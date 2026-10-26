package identityservice

import (
	"bytes"
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/pki"
	"github.com/thtpark/fdn/internal/secure"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const deviceFile = "device.json"
const profileFile = "connection.json"
const certificateFile = "device-certificate.json"
const activationFile = "activation.json"
const sshKeyFile = "ssh-private.pem"

// Options injects account-contained crypto and transport boundaries for tests.
// There are deliberately no JSON settings or native flags selecting test UIDs.
type Options struct {
	Remote            Remote
	Cryptography      Cryptography
	Now               func() time.Time
	RetryInterval     time.Duration
	RenewInterval     time.Duration
	DeviceRenewBefore time.Duration
	Log               *slog.Logger
}

func (o Options) defaults() Options {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Remote == nil {
		o.Remote = NativeRemote{}
	}
	if o.Cryptography == nil {
		o.Cryptography = nativeCryptography{}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.RetryInterval <= 0 {
		o.RetryInterval = time.Second
	}
	if o.RenewInterval <= 0 {
		o.RenewInterval = 5 * time.Minute
	}
	if o.DeviceRenewBefore <= 0 {
		o.DeviceRenewBefore = 5 * time.Minute
	}
	return o
}

type privateDevice struct {
	Public      domain.PublicIdentity `json:"public"`
	SSHPrivate  []byte                `json:"ssh_private"`
	AuthPrivate []byte                `json:"auth_private"`
}

type pendingActivation struct {
	InstanceID string                         `json:"instance_id"`
	AgentID    string                         `json:"agent_id"`
	Generation int64                          `json:"generation"`
	Request    domain.ActivateIdentityRequest `json:"request"`
}

func inside(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func validateSettings(c config.IdentityService) error {
	if !filepath.IsAbs(c.StateDir) || !filepath.IsAbs(c.Socket) || c.WorkerUID < 0 || c.WorkerGID < 0 ||
		strings.ContainsAny(c.StateDir+c.Socket, "\x00\r\n") || filepath.Clean(c.StateDir) != c.StateDir || filepath.Clean(c.Socket) != c.Socket {
		return domain.Fail(domain.Invalid, "identity service needs clean absolute paths and explicit worker UID/GID")
	}
	if inside(c.StateDir, c.Socket) || inside(filepath.Dir(c.Socket), c.StateDir) {
		return domain.Fail(domain.Denied, "identity private state and worker-accessible socket directory must be separate")
	}
	return nil
}

// checkAncestors rejects symlink and writable-parent substitution of private
// state or the socket directory. Neither path may be controlled by the worker.
func checkAncestors(path string) error {
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 ||
			(stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) {
			return domain.Fail(domain.Denied, "identity paths need real ancestor directories controlled by this account or root, without group/other writes")
		}
		if parent == filepath.Dir(parent) {
			return nil
		}
	}
}

func stateLock(dir string) (*secure.Lock, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return nil, domain.Fail(domain.Invalid, "identity state must be an absolute clean directory")
	}
	if err := checkAncestors(dir); err != nil {
		return nil, err
	}
	return secure.Acquire(filepath.Join(dir, "identity.lock"))
}

func loadDevice(dir string, cryptography Cryptography) (privateDevice, crypto.Signer, error) {
	var d privateDevice
	if err := config.Read(filepath.Join(dir, deviceFile), &d); err != nil {
		return d, nil, err
	}
	auth, err := cryptography.ParseSigner(d.AuthPrivate)
	if err != nil {
		return d, nil, domain.Fail(domain.Denied, "identity authentication key is invalid")
	}
	public, err := x509.MarshalPKIXPublicKey(auth.Public())
	if err != nil || d.Public.AuthKey != string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: public})) {
		return d, nil, domain.Fail(domain.Denied, "stored authentication key does not match the public identity")
	}
	sshSigner, err := ssh.ParsePrivateKey(d.SSHPrivate)
	if err != nil || strings.TrimSpace(d.Public.SSHKey) != strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshSigner.PublicKey()))) {
		return d, nil, domain.Fail(domain.Denied, "stored SSH key does not match the public identity")
	}
	return d, auth, nil
}

// Prepare is an offline identity-account operation. The retained private bundle
// is committed before public output, making a lost output an exact retry rather
// than a second device. It refuses to run while serve/connect holds the lock.
func Prepare(stateDir, name, output string, options Options) (domain.PublicIdentity, error) {
	var result domain.PublicIdentity
	if err := domain.CheckText(name, "identity name", 128, true); err != nil {
		return result, err
	}
	if !filepath.IsAbs(output) || inside(stateDir, output) || strings.ContainsAny(output, "\x00\r\n") {
		return result, domain.Fail(domain.Invalid, "public output must be an absolute path outside private identity state")
	}
	lock, err := stateLock(stateDir)
	if err != nil {
		return result, err
	}
	defer lock.Close()
	options = options.defaults()
	var d privateDevice
	err = config.Read(filepath.Join(stateDir, deviceFile), &d)
	if errors.Is(err, os.ErrNotExist) {
		d.Public, d.SSHPrivate, d.AuthPrivate, err = options.Cryptography.Generate(name)
		if err != nil {
			return result, err
		}
		if err := config.Write(filepath.Join(stateDir, deviceFile), d); err != nil {
			return result, err
		}
	} else if err != nil {
		return result, err
	}
	if d.Public.Name != name {
		return result, domain.Fail(domain.Conflict, "this account already prepared a different identity name")
	}
	if _, _, err := loadDevice(stateDir, options.Cryptography); err != nil {
		return result, err
	}
	if err := ensurePrivate(filepath.Join(stateDir, sshKeyFile), d.SSHPrivate); err != nil {
		return result, err
	}
	if err := checkAncestors(output); err != nil {
		return result, err
	}
	var previous domain.PublicIdentity
	if err := config.Read(output, &previous); err == nil {
		if !reflect.DeepEqual(previous, d.Public) {
			return result, domain.Fail(domain.Conflict, "public output already contains different material")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := config.Write(output, d.Public); err != nil {
			return result, err
		}
	} else {
		return result, err
	}
	return d.Public, nil
}

func ensurePrivate(path string, expected []byte) error {
	actual, err := secure.Read(path, domain.MaxBody)
	if errors.Is(err, os.ErrNotExist) {
		return secure.Write(path, expected)
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, expected) {
		return domain.Fail(domain.Conflict, "retained identity key file differs from its prepared device")
	}
	return nil
}

var accountName = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,63}$`)

func validateProfile(p config.ConnectionProfile, d privateDevice) error {
	i := p.Identity
	if i.AgentID == "" || i.InstanceID == "" || i.RequestID != d.Public.RequestID || i.Generation < 1 || i.UID <= 0 ||
		(i.State != "provisioned" && i.State != "active") || i.BanReason != "" || i.SSHUser == "root" || !accountName.MatchString(i.SSHUser) ||
		strings.TrimSpace(i.SSHKey) != strings.TrimSpace(d.Public.SSHKey) || i.AuthKey != d.Public.AuthKey {
		return domain.Fail(domain.Denied, "connection profile is not the provisioned public identity prepared by this account")
	}
	if err := config.URL(p.URL); err != nil {
		return err
	}
	if p.SSHPort < 1 || p.SSHPort > 65535 || p.SSHHost == "" || strings.HasPrefix(p.SSHHost, "-") ||
		strings.ContainsAny(p.SSHHost, " /\\\x00\r\n\t@") || len(p.SSHHost) > 253 {
		return domain.Fail(domain.Invalid, "connection profile needs an explicit SSH host and port")
	}
	pool := x509.NewCertPool()
	if p.CA == "" || !pool.AppendCertsFromPEM([]byte(p.CA)) || strings.TrimSpace(p.KnownHosts) == "" {
		return domain.Fail(domain.Invalid, "connection profile requires pinned CA and SSH host trust")
	}
	for _, line := range strings.Split(p.KnownHosts, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if _, _, _, _, _, err := ssh.ParseKnownHosts([]byte(line)); err != nil {
			return domain.Fail(domain.Invalid, "connection profile contains invalid SSH host pins")
		}
	}
	return nil
}

func bindProfile(c config.IdentityService, p config.ConnectionProfile, d privateDevice) (config.Worker, error) {
	var worker config.Worker
	if err := validateProfile(p, d); err != nil {
		return worker, err
	}
	var previous config.ConnectionProfile
	if err := config.Read(filepath.Join(c.StateDir, profileFile), &previous); err == nil {
		if !reflect.DeepEqual(previous, p) {
			return worker, domain.Fail(domain.Conflict, "connection trust/identity is already pinned; explicit recovery is required to change it")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return worker, err
	}
	if err := ensurePrivate(filepath.Join(c.StateDir, sshKeyFile), d.SSHPrivate); err != nil {
		return worker, err
	}
	for _, file := range []struct{ name, contents string }{{"ca.pem", p.CA}, {"known_hosts", p.KnownHosts}} {
		if err := ensurePrivate(filepath.Join(c.StateDir, file.name), []byte(file.contents)); err != nil {
			return worker, err
		}
	}
	if _, err := knownhosts.New(filepath.Join(c.StateDir, "known_hosts")); err != nil {
		return worker, domain.Fail(domain.Invalid, "connection profile contains invalid SSH host pins")
	}
	if err := config.Write(filepath.Join(c.StateDir, profileFile), p); err != nil {
		return worker, err
	}
	worker = config.DefaultWorker(c.StateDir)
	worker.URL = strings.TrimRight(p.URL, "/")
	u, _ := url.Parse(p.URL)
	worker.ServerName = u.Hostname()
	worker.CAFile = filepath.Join(c.StateDir, "ca.pem")
	worker.SSH = &config.SSH{Host: p.SSHHost, Port: p.SSHPort, User: p.Identity.SSHUser,
		IdentityFile: filepath.Join(c.StateDir, sshKeyFile), KnownHostsFile: filepath.Join(c.StateDir, "known_hosts")}
	return worker, nil
}

func validateCertificate(certificate string, keyPEM []byte, p config.ConnectionProfile, purpose string, generation int64, now time.Time) (tls.Certificate, error) {
	pair, err := tls.X509KeyPair([]byte(certificate), keyPEM)
	if err != nil || len(pair.Certificate) == 0 {
		return tls.Certificate{}, domain.Fail(domain.Denied, "issued certificate does not match the account-contained signing key")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return tls.Certificate{}, domain.Fail(domain.Denied, "issued identity certificate is invalid")
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(p.CA))
	intermediates := x509.NewCertPool()
	for _, der := range pair.Certificate[1:] {
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return tls.Certificate{}, domain.Fail(domain.Denied, "issued identity certificate chain is invalid")
		}
		intermediates.AddCert(cert)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return tls.Certificate{}, domain.Fail(domain.Denied, "issued identity certificate is not valid under pinned trust")
	}
	identity, err := pki.ParseIdentity(leaf)
	if err != nil || identity.AgentID != p.Identity.AgentID || identity.InstanceID != p.Identity.InstanceID ||
		identity.Generation != p.Identity.Generation || identity.Purpose != purpose || identity.OwnerGeneration != generation {
		return tls.Certificate{}, domain.Fail(domain.Denied, "issued certificate does not match the pinned identity and owner generation")
	}
	pair.Leaf = leaf
	return pair, nil
}

func activate(ctx context.Context, c config.IdentityService, p config.ConnectionProfile, d privateDevice, signer crypto.Signer, worker config.Worker, options Options) (domain.IdentityCertificate, tls.Certificate, error) {
	var saved domain.IdentityCertificate
	if err := config.Read(filepath.Join(c.StateDir, certificateFile), &saved); err == nil {
		if sameIdentity(saved.Identity, p.Identity) && saved.Agent.ID == p.Identity.AgentID && saved.Agent.Active && !saved.Agent.Banned {
			pair, err := validateCertificate(saved.Certificate, d.AuthPrivate, p, "device", 0, options.Now())
			if err == nil && pair.Leaf.NotAfter.After(options.Now().Add(options.DeviceRenewBefore)) {
				if err := clearActivation(c.StateDir); err != nil {
					return saved, tls.Certificate{}, err
				}
				return saved, pair, nil
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return saved, tls.Certificate{}, err
	}
	var pending pendingActivation
	pendingPath := filepath.Join(c.StateDir, activationFile)
	if err := config.Read(pendingPath, &pending); errors.Is(err, os.ErrNotExist) {
		challenge, err := options.Remote.Challenge(ctx, worker)
		if err != nil {
			return saved, tls.Certificate{}, err
		}
		if challenge.AgentID != p.Identity.AgentID || challenge.InstanceID != p.Identity.InstanceID ||
			challenge.Generation != p.Identity.Generation || challenge.ExpiresAt <= options.Now().Unix() || challenge.Nonce == "" {
			return saved, tls.Certificate{}, domain.Fail(domain.Denied, "activation challenge does not match pinned identity or has expired")
		}
		proof, err := options.Cryptography.SignChallenge(signer, challenge)
		if err != nil {
			return saved, tls.Certificate{}, err
		}
		pending = pendingActivation{InstanceID: p.Identity.InstanceID, AgentID: p.Identity.AgentID, Generation: p.Identity.Generation,
			Request: domain.ActivateIdentityRequest{Command: domain.Command{OperationID: domain.NewID("activate")}, Nonce: challenge.Nonce, Proof: proof}}
		if err := config.Write(pendingPath, pending); err != nil {
			return saved, tls.Certificate{}, err
		}
	} else if err != nil {
		return saved, tls.Certificate{}, err
	}
	if pending.InstanceID != p.Identity.InstanceID || pending.AgentID != p.Identity.AgentID || pending.Generation != p.Identity.Generation {
		return saved, tls.Certificate{}, domain.Fail(domain.Conflict, "pending activation belongs to another pinned identity")
	}
	issued, err := options.Remote.Activate(ctx, worker, pending.Request)
	if err != nil {
		return saved, tls.Certificate{}, err
	}
	if !sameIdentity(issued.Identity, p.Identity) || issued.Agent.ID != p.Identity.AgentID || issued.Agent.Banned || !issued.Agent.Active {
		return saved, tls.Certificate{}, domain.Fail(domain.Denied, "activation response changed the pinned identity")
	}
	pair, err := validateCertificate(issued.Certificate, d.AuthPrivate, p, "device", 0, options.Now())
	if err != nil {
		return saved, tls.Certificate{}, err
	}
	if !pair.Leaf.NotAfter.After(options.Now().Add(options.DeviceRenewBefore)) {
		return saved, tls.Certificate{}, domain.Fail(domain.Prerequisite, "issued device certificate does not leave the required renewal window")
	}
	if err := config.Write(filepath.Join(c.StateDir, certificateFile), issued); err != nil {
		return saved, tls.Certificate{}, err
	}
	if err := clearActivation(c.StateDir); err != nil {
		return saved, tls.Certificate{}, err
	}
	return issued, pair, nil
}

func clearActivation(dir string) error {
	if err := os.Remove(filepath.Join(dir, activationFile)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return secure.SyncDir(dir)
}

func sameIdentity(a, b domain.AgentIdentity) bool {
	return a.AgentID == b.AgentID && a.InstanceID == b.InstanceID && a.RequestID == b.RequestID && a.Generation == b.Generation &&
		a.SSHUser == b.SSHUser && a.UID == b.UID && a.AuthKey == b.AuthKey && strings.TrimSpace(a.SSHKey) == strings.TrimSpace(b.SSHKey)
}

// Connect pins an integrity-approved, public-only profile and activates this
// account's existing device. Config.Read must be used on the protected input.
func Connect(ctx context.Context, c config.IdentityService, p config.ConnectionProfile, options Options) (api.LocalStatus, error) {
	var status api.LocalStatus
	if err := validateSettings(c); err != nil {
		return status, err
	}
	lock, err := stateLock(c.StateDir)
	if err != nil {
		return status, err
	}
	defer lock.Close()
	options = options.defaults()
	d, signer, err := loadDevice(c.StateDir, options.Cryptography)
	if err != nil {
		return status, err
	}
	worker, err := bindProfile(c, p, d)
	if err != nil {
		return status, err
	}
	issued, _, err := activate(ctx, c, p, d, signer, worker, options)
	if err != nil {
		return status, err
	}
	return api.LocalStatus{Agent: issued.Agent, Identity: issued.Identity, Connected: true}, nil
}

func ownerKeyPEM(signer crypto.Signer) ([]byte, error) {
	key, err := x509.MarshalPKCS8PrivateKey(signer)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), nil
}
