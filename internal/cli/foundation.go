package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"time"

	"github.com/thtpark/fdn/internal/backup"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/foundation"
	"github.com/thtpark/fdn/internal/pki"
	"github.com/thtpark/fdn/internal/secure"
)

func (a *app) foundation(ctx context.Context, args []string) (any, error) {
	switch args[0] {
	case "upgrade-helper":
		return a.upgradeHelper(ctx, args[1:])
	case "request-worker":
		return a.requestWorker(ctx, args[1:])
	case "init":
		return a.initFoundation(ctx, args[1:])
	case "serve":
		return a.serveFoundation(ctx, args[1:])
	case "gateway":
		return a.nativeGateway(ctx, args[1:])
	case "accounts":
		return a.nativeAccounts(ctx, args[1:])
	case "recover-founder":
		return a.recoverNativeFounder(ctx, args[1:])
	case "bootstrap":
		return nil, domain.Fail(domain.Prerequisite, "bearer bootstrap is retired; use a protected public Founder identity request")
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
		return map[string]any{"restored": err == nil, "state_dir": destinationAbs, "note": "restored identities and owners are quarantined; explicit local root Founder recovery with fresh proved keys is required, followed by fresh Founder reapproval of agents; active runs remain fenced; native account, group and service reconciliation is separate"}, err
	case "service":
		return a.nativeService(ctx, args[1:], "foundation")
	case "doctor", "backup":
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
			if err := validateNativeServer(c); err != nil {
				return nil, err
			}
			if _, err := pki.ServerMutual(c.CertFile, c.KeyFile, filepath.Join(c.StateDir, "ca.pem")); err != nil {
				return nil, err
			}
			return map[string]any{"database": "ok", "tls_key_pair": "ok", "schema_version": domain.SchemaVersion, "ingress_socket": c.IngressSocket, "join_socket": c.JoinSocket}, nil
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

func (a *app) initFoundation(ctx context.Context, args []string) (any, error) {
	return a.initNativeFoundation(ctx, args, nil)
}

func (a *app) serveFoundation(ctx context.Context, args []string) (any, error) {
	return a.serveNativeFoundation(ctx, args, nil)
}
