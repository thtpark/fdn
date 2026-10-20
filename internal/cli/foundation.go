package cli

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/backup"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/pki"
	"github.com/thtpark/fdn/internal/secure"
)

func (a *app) foundation(ctx context.Context, args []string) (any, error) {
	switch args[0] {
	case "init":
		return a.initFoundation(ctx, args[1:])
	case "serve":
		return a.serveFoundation(ctx, args[1:])
	case "tls-renew":
		if len(args) != 1 {
			return nil, domain.Fail(domain.Invalid, "tls-renew has no leaf flags")
		}
		c, err := config.LoadServer(a.configFile)
		if err != nil {
			return nil, err
		}
		lock, err := secure.Acquire(filepath.Join(c.StateDir, "foundation.lock"))
		if err != nil {
			return nil, domain.Fail(domain.Prerequisite, "stop Foundation before renewing its server key/certificate pair")
		}
		defer lock.Close()
		err = pki.Renew(c.StateDir, c.CertFile, c.KeyFile, time.Now())
		return map[string]bool{"renewed": err == nil, "ca_and_enrolled_identities_preserved": true}, err
	case "backup-key":
		fs := flag.NewFlagSet("backup-key", flag.ContinueOnError)
		output := fs.String("output", "", "private age identity file")
		if err := parse(fs, args[1:]); err != nil {
			return nil, err
		}
		if *output == "" {
			return nil, domain.Fail(domain.Invalid, "--output is required")
		}
		recipient, err := backup.Keygen(*output)
		return map[string]string{"recipient": recipient, "identity_file": *output}, err
	case "restore":
		fs := flag.NewFlagSet("restore", flag.ContinueOnError)
		input := fs.String("input", "", "encrypted backup")
		identity := fs.String("identity-file", "", "private age identity")
		destination := fs.String("destination", "", "new private state directory")
		if err := parse(fs, args[1:]); err != nil {
			return nil, err
		}
		if *input == "" || *identity == "" || *destination == "" {
			return nil, domain.Fail(domain.Invalid, "--input, --identity-file and --destination are required")
		}
		destinationAbs, err := filepath.Abs(*destination)
		if err != nil {
			return nil, err
		}
		err = backup.Restore(ctx, *input, *identity, destinationAbs)
		return map[string]any{"restored": err == nil, "state_dir": destinationAbs, "note": "active runs remain fenced; sessions are retired; OS service registration is separate"}, err
	case "service":
		return a.nativeService(ctx, args[1:], "foundation")
	case "doctor", "backup", "bootstrap":
		c, err := config.LoadServer(a.configFile)
		if err != nil {
			return nil, err
		}
		store, err := foundation.Open(ctx, filepath.Join(c.StateDir, "foundation.sqlite"))
		if err != nil {
			return nil, err
		}
		defer store.Close()
		if args[0] == "doctor" {
			if len(args) != 1 {
				return nil, domain.Fail(domain.Invalid, "doctor has no leaf flags")
			}
			if err := store.Check(ctx); err != nil {
				return nil, err
			}
			if _, err := pki.Server(c.CertFile, c.KeyFile); err != nil {
				return nil, err
			}
			return map[string]any{"database": "ok", "tls_key_pair": "ok", "schema_version": domain.SchemaVersion, "listen": c.Listen}, nil
		}
		if args[0] == "bootstrap" {
			fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
			output := fs.String("output", "", "new one-time bootstrap file")
			ttl := fs.Int("ttl", 900, "expiry seconds")
			if err := parse(fs, args[1:]); err != nil {
				return nil, err
			}
			if *output == "" || *ttl < 60 || *ttl > 86400 {
				return nil, domain.Fail(domain.Invalid, "--output and a 60-86400 second --ttl are required")
			}
			var bundle config.InvitationBundle
			readErr := config.Read(*output, &bundle)
			if os.IsNotExist(readErr) {
				bundle = config.InvitationBundle{Token: domain.NewToken(), OperationID: a.operation, ExpiresAt: time.Now().Unix() + int64(*ttl)}
				if err := privateExact(*output, bundle); err != nil {
					return nil, err
				}
			} else if readErr != nil {
				return nil, readErr
			}
			if a.explicitOperation && a.operation != bundle.OperationID {
				return nil, domain.Fail(domain.Conflict, "bootstrap file belongs to another operation")
			}
			a.operation = bundle.OperationID
			inv, err := store.RotateBootstrap(ctx, domain.Hash(bundle.Token), bundle.ExpiresAt, a.operation)
			return map[string]any{"invitation": inv, "secret_file": *output}, err
		}
		fs := flag.NewFlagSet("backup", flag.ContinueOnError)
		output := fs.String("output", "", "encrypted backup file")
		recipient := fs.String("recipient", "", "public age recipient")
		if err := parse(fs, args[1:]); err != nil {
			return nil, err
		}
		if *output == "" || *recipient == "" {
			return nil, domain.Fail(domain.Invalid, "--output and --recipient are required")
		}
		err = backup.Create(ctx, store, c, *output, *recipient)
		return map[string]any{"created": err == nil, "encrypted": true, "output": *output}, err
	default:
		return nil, domain.Fail(domain.Invalid, "unknown Foundation command")
	}
}

func privateExact(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := secure.Create(path, data); err != nil {
		if !os.IsExist(err) {
			return err
		}
		existing, err := secure.Read(path, domain.MaxBody)
		if err != nil {
			return err
		}
		if !bytes.Equal(existing, data) {
			return domain.Fail(domain.Conflict, "existing private file differs; refusing to overwrite identity or bootstrap material")
		}
	}
	return nil
}

type initPlan struct {
	Config        config.Server           `json:"config"`
	PKI           pki.Bundle              `json:"pki"`
	Bootstrap     config.InvitationBundle `json:"bootstrap"`
	BootstrapFile string                  `json:"bootstrap_file"`
}

func (a *app) initFoundation(ctx context.Context, args []string) (any, error) {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	state := fs.String("state", "", "private Foundation directory")
	listen := fs.String("listen", "127.0.0.1:8443", "loopback TLS listener")
	bootstrapFile := fs.String("bootstrap-file", "", "owner-only bootstrap output")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	if *state == "" || *bootstrapFile == "" {
		return nil, domain.Fail(domain.Invalid, "--state and --bootstrap-file are required")
	}
	root, err := filepath.Abs(*state)
	if err != nil {
		return nil, err
	}
	c := config.Server{StateDir: root, Listen: *listen, CertFile: filepath.Join(root, "server.pem"), KeyFile: filepath.Join(root, "server-key.pem")}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if err := secure.Dir(root); err != nil {
		return nil, err
	}
	lock, err := secure.Acquire(filepath.Join(root, "foundation.lock"))
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	existing, err := config.LoadServer(a.configFile)
	if err == nil {
		if existing != c {
			return nil, domain.Fail(domain.Conflict, "existing configuration differs; initialization never overwrites state")
		}
		return map[string]any{"initialized": true, "changed": false, "config": a.configFile, "ca_file": filepath.Join(root, "ca.pem")}, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	planPath := filepath.Join(root, "init-plan.json")
	var plan initPlan
	err = config.Read(planPath, &plan)
	if os.IsNotExist(err) {
		bundle, err := pki.Generate(time.Now())
		if err != nil {
			return nil, err
		}
		plan = initPlan{Config: c, PKI: bundle, Bootstrap: config.InvitationBundle{Token: domain.NewToken(), OperationID: "bootstrap", ExpiresAt: time.Now().Unix() + 900}, BootstrapFile: *bootstrapFile}
		if err := config.Write(planPath, plan); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if plan.Config != c || plan.BootstrapFile != *bootstrapFile {
		return nil, domain.Fail(domain.Conflict, "resume initialization with its exact original paths/listener")
	}
	for _, file := range []struct {
		name string
		data []byte
	}{{"ca.pem", plan.PKI.CA}, {"ca-key.pem", plan.PKI.CAKey}, {"server.pem", plan.PKI.Cert}, {"server-key.pem", plan.PKI.Key}} {
		path := filepath.Join(root, file.name)
		if err := secure.Create(path, file.data); err != nil {
			if !os.IsExist(err) {
				return nil, err
			}
			existing, readErr := secure.Read(path, domain.MaxBody)
			if readErr != nil {
				return nil, readErr
			}
			if !bytes.Equal(existing, file.data) {
				return nil, domain.Fail(domain.Conflict, "existing TLS files differ from the retained initialization plan")
			}
		}
	}
	store, err := foundation.Open(ctx, filepath.Join(root, "foundation.sqlite"))
	if err != nil {
		return nil, err
	}
	defer store.Close()
	if _, err := store.Bootstrap(ctx, domain.Hash(plan.Bootstrap.Token), plan.Bootstrap.ExpiresAt); err != nil {
		return nil, err
	}
	if err := privateExact(*bootstrapFile, plan.Bootstrap); err != nil {
		return nil, err
	}
	if err := privateExact(a.configFile, c); err != nil {
		return nil, err
	}
	if err := os.Remove(planPath); err != nil {
		return nil, err
	}
	return map[string]any{"initialized": true, "config": a.configFile, "ca_file": filepath.Join(root, "ca.pem"), "bootstrap_file": *bootstrapFile, "bootstrap_expires_at": plan.Bootstrap.ExpiresAt}, nil
}

func (a *app) serveFoundation(ctx context.Context, args []string) (any, error) {
	if len(args) != 0 {
		return nil, domain.Fail(domain.Invalid, "serve has no leaf flags")
	}
	c, err := config.LoadServer(a.configFile)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(c.StateDir, "tls-renewal.json")); err == nil {
		return nil, domain.Fail(domain.Prerequisite, "complete the retained tls-renew operation before serving")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	lock, err := secure.Acquire(filepath.Join(c.StateDir, "foundation.lock"))
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	store, err := foundation.Open(ctx, filepath.Join(c.StateDir, "foundation.sqlite"))
	if err != nil {
		return nil, err
	}
	defer store.Close()
	service, err := api.New(store, slog.New(slog.NewJSONHandler(a.diagnostic, nil)))
	if err != nil {
		return nil, err
	}
	defer service.Close()
	tlsConfig, err := pki.Server(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", c.Listen)
	if err != nil {
		return nil, err
	}
	server := &http.Server{Handler: service.Handler(), TLSConfig: tlsConfig, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: slog.NewLogLogger(service.Log.Handler(), slog.LevelWarn)}
	done := make(chan error, 1)
	go func() { done <- server.Serve(tls.NewListener(listener, tlsConfig)) }()
	a.emit(map[string]any{"state": "listening", "address": listener.Addr().String(), "version": domain.Version}, nil)
	select {
	case err := <-done:
		return nil, err
	case <-ctx.Done():
		service.Close()
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		stopErr := server.Shutdown(stopCtx)
		serveErr := <-done
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return map[string]string{"state": "stopped"}, errors.Join(stopErr, serveErr)
	}
}
