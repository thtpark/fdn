package platform

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
)

// NewNativeAccountManager plans without mutating the host. Each actual Apply,
// Reconcile or Disable additionally requires apply=true, Linux and effective
// root. Native commands are fixed absolute executables with a minimal environment.
func NewNativeAccountManager(options AccountOptions, runner process.Runner) (*AccountManager, error) {
	if err := options.normalize(); err != nil {
		return nil, err
	}
	if runner == nil {
		runner = process.Native{}
	}
	manager, err := NewAccountManager(options, &nativeAccountSystem{options: options, runner: runner})
	if err != nil {
		return nil, err
	}
	manager.native = true
	return manager, nil
}

type nativeAccountSystem struct {
	options AccountOptions
	runner  process.Runner
}

func (s *nativeAccountSystem) run(ctx context.Context, path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	output, diagnostic := process.NewBuffer(64<<10), process.NewBuffer(8<<10)
	err := s.runner.Run(ctx, process.Spec{Path: path, Args: args,
		Env: []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "HOME=/root"}, Stdout: output, Stderr: diagnostic})
	data, outputErr := output.Bytes()
	_, diagnosticErr := diagnostic.Bytes()
	if err != nil {
		return nil, errors.Join(err, outputErr, diagnosticErr)
	}
	return data, errors.Join(outputErr, diagnosticErr)
}

func (s *nativeAccountSystem) lookup(ctx context.Context, database, key string) (string, bool, error) {
	data, err := s.run(ctx, "/usr/bin/getent", "--", database, key)
	if err != nil {
		var status interface{ ExitCode() int }
		if errors.As(err, &status) && status.ExitCode() == 2 {
			return "", false, nil
		}
		return "", false, err
	}
	line := strings.TrimSuffix(string(data), "\n")
	if line == "" || strings.ContainsAny(line, "\r\n\x00") {
		return "", false, errors.New("ambiguous native account lookup")
	}
	return line, true, nil
}

func (s *nativeAccountSystem) LookupUser(ctx context.Context, name string) (AccountUser, bool, error) {
	return s.lookupUser(ctx, name)
}

func (s *nativeAccountSystem) LookupUID(ctx context.Context, uid uint32) (AccountUser, bool, error) {
	return s.lookupUser(ctx, strconv.FormatUint(uint64(uid), 10))
}

func (s *nativeAccountSystem) lookupUser(ctx context.Context, key string) (AccountUser, bool, error) {
	line, exists, err := s.lookup(ctx, "passwd", key)
	if err != nil || !exists {
		return AccountUser{}, exists, err
	}
	fields := strings.Split(line, ":")
	if len(fields) != 7 || !username.MatchString(fields[0]) {
		return AccountUser{}, false, errors.New("invalid native passwd record")
	}
	uid, uidErr := strconv.ParseUint(fields[2], 10, 32)
	gid, gidErr := strconv.ParseUint(fields[3], 10, 32)
	if err := errors.Join(uidErr, gidErr); err != nil {
		return AccountUser{}, false, err
	}
	groups, err := s.run(ctx, "/usr/bin/id", "--groups", "--", fields[0])
	if err != nil {
		return AccountUser{}, false, err
	}
	user := AccountUser{Username: fields[0], UID: uint32(uid), GID: uint32(gid), Marker: fields[4], Home: fields[5], Shell: fields[6]}
	for _, group := range strings.Fields(string(groups)) {
		id, err := strconv.ParseUint(group, 10, 32)
		if err != nil {
			return AccountUser{}, false, err
		}
		user.Groups = append(user.Groups, uint32(id))
	}
	if len(user.Groups) == 0 {
		return AccountUser{}, false, errors.New("native account has no primary group")
	}
	return user, true, nil
}

func (s *nativeAccountSystem) LookupGroup(ctx context.Context, name string) (AccountGroup, bool, error) {
	return s.lookupGroup(ctx, name)
}

func (s *nativeAccountSystem) LookupGID(ctx context.Context, gid uint32) (AccountGroup, bool, error) {
	return s.lookupGroup(ctx, strconv.FormatUint(uint64(gid), 10))
}

func (s *nativeAccountSystem) lookupGroup(ctx context.Context, key string) (AccountGroup, bool, error) {
	line, exists, err := s.lookup(ctx, "group", key)
	if err != nil || !exists {
		return AccountGroup{}, exists, err
	}
	fields := strings.Split(line, ":")
	if len(fields) != 4 || !username.MatchString(fields[0]) {
		return AccountGroup{}, false, errors.New("invalid native group record")
	}
	gid, err := strconv.ParseUint(fields[2], 10, 32)
	if err != nil {
		return AccountGroup{}, false, err
	}
	group := AccountGroup{Name: fields[0], GID: uint32(gid)}
	if fields[3] != "" {
		group.Members = strings.Split(fields[3], ",")
	}
	return group, true, nil
}

func (s *nativeAccountSystem) validUser(user AccountUser) error {
	agentID := strings.TrimPrefix(user.Marker, "fdn-agent=")
	name, err := accountUsername(agentID, user.Username)
	if err != nil || user.Username == "" || user.UID < s.options.UIDMin || user.UID > s.options.UIDMax ||
		user.UID == s.options.ServerUID || user.GID != s.options.IngressGID ||
		user.Home != filepath.Join(s.options.PolicyDir, "homes", name) || user.Shell != "/bin/sh" ||
		len(user.Groups) != 1 || user.Groups[0] != s.options.IngressGID {
		return ErrAccountConflict
	}
	return nil
}

func (s *nativeAccountSystem) CreateGroup(ctx context.Context, group AccountGroup) error {
	if group.Name != s.options.IngressGroup || group.GID != s.options.IngressGID || len(group.Members) != 0 {
		return ErrAccountConflict
	}
	_, err := s.run(ctx, "/usr/sbin/groupadd", "--system", "--gid", strconv.FormatUint(uint64(group.GID), 10), "--", group.Name)
	return err
}

func (s *nativeAccountSystem) GrantServerIngress(ctx context.Context, user AccountUser) error {
	if user.UID != s.options.ServerUID || user.UID == 0 || user.GID != s.options.ServerGID || !username.MatchString(user.Username) {
		return ErrAccountConflict
	}
	actual, exists, err := s.LookupUID(ctx, s.options.ServerUID)
	if err != nil || !exists || actual.Username != user.Username || actual.GID != s.options.ServerGID {
		return errors.Join(ErrAccountConflict, err)
	}
	_, err = s.run(ctx, "/usr/sbin/usermod", "--append", "--groups", s.options.IngressGroup, "--", user.Username)
	return err
}

func (s *nativeAccountSystem) CreateUser(ctx context.Context, user AccountUser) error {
	if err := s.validUser(user); err != nil {
		return err
	}
	_, err := s.run(ctx, "/usr/sbin/useradd", "--system", "--no-user-group", "--no-create-home", "--uid", strconv.FormatUint(uint64(user.UID), 10),
		"--gid", strconv.FormatUint(uint64(user.GID), 10), "--groups", "", "--home-dir", user.Home, "--shell", "/bin/sh", "--comment", user.Marker,
		"--password", "*", "--expiredate", "1970-01-02", "--", user.Username)
	return err
}

func (s *nativeAccountSystem) ConfigureUser(ctx context.Context, user AccountUser) error {
	if err := s.validUser(user); err != nil {
		return err
	}
	_, err := s.run(ctx, "/usr/sbin/usermod", "--lock", "--expiredate", "1970-01-02", "--gid", strconv.FormatUint(uint64(user.GID), 10),
		"--groups", "", "--home", user.Home, "--shell", "/bin/sh", "--comment", user.Marker, "--", user.Username)
	return err
}

func (s *nativeAccountSystem) DisableUser(ctx context.Context, user AccountUser) error {
	if err := s.validUser(user); err != nil {
		return err
	}
	_, err := s.run(ctx, "/usr/sbin/usermod", "--lock", "--expiredate", "1970-01-02", "--shell", "/usr/sbin/nologin", "--gid", strconv.FormatUint(uint64(user.GID), 10), "--groups", "", "--", user.Username)
	return err
}

func (s *nativeAccountSystem) EnableUser(ctx context.Context, user AccountUser) error {
	if err := s.validUser(user); err != nil {
		return err
	}
	// '*' is an impossible password, not a locked shadow account: public-key
	// authentication must still work on sshd installations without PAM.
	_, err := s.run(ctx, "/usr/sbin/usermod", "--password", "*", "--expiredate", "", "--shell", "/bin/sh", "--", user.Username)
	return err
}

func (s *nativeAccountSystem) ActivateSSH(ctx context.Context, policy AccountSSHPolicy) error {
	if !domain.ValidAccountUsername(policy.Username) {
		return ErrAccountConflict
	}
	manager := &AccountManager{options: s.options}
	if policy != manager.sshPolicy(policy.Username) {
		return ErrAccountConflict
	}
	if err := accountTrustedFile(policy.MainConfig, 0, false); err != nil {
		return err
	}
	if _, err := s.run(ctx, "/usr/sbin/sshd", "-t", "-f", policy.MainConfig); err != nil {
		return errors.New("native sshd rejected managed policy; account remains disabled")
	}
	output, err := s.run(ctx, "/usr/sbin/sshd", "-T", "-f", policy.MainConfig, "-C", "user="+policy.Username+",host=localhost,addr=127.0.0.1,laddr=127.0.0.1,lport=22")
	if err != nil {
		return err
	}
	if err := checkEffectiveAccountSSH(output, policy); err != nil {
		return err
	}
	_, err = s.run(ctx, "/usr/bin/systemctl", "reload", s.options.SSHDService)
	return err
}

func checkEffectiveAccountSSH(output []byte, policy AccountSSHPolicy) error {
	values := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(output), "\n"), "\n") {
		name, value, ok := strings.Cut(line, " ")
		if !ok || strings.ContainsAny(line, "\x00\r") {
			return errors.New("invalid native sshd effective policy")
		}
		values[name] = append(values[name], value)
	}
	expected := map[string]string{
		"authenticationmethods": "publickey", "pubkeyauthentication": "yes", "passwordauthentication": "no", "kbdinteractiveauthentication": "no",
		"authorizedkeysfile": policy.AuthorizedKeysFile, "authorizedkeyscommand": "none", "authorizedprincipalsfile": "none", "authorizedprincipalscommand": "none",
		"trustedusercakeys": "none", "forcecommand": policy.ForcedCommand, "disableforwarding": "yes", "allowtcpforwarding": "no",
		"allowstreamlocalforwarding": "no", "allowagentforwarding": "no", "x11forwarding": "no", "permittunnel": "no", "permittty": "no",
		"permituserrc": "no", "permituserenvironment": "no", "permitopen": "none", "permitlisten": "none", "maxsessions": "1", "acceptenv": "FDN_GATEWAY_UNUSED",
	}
	for name, expected := range expected {
		if len(values[name]) != 1 || values[name][0] != expected {
			return fmt.Errorf("sshd does not enforce managed ingress %s; account remains disabled", name)
		}
	}
	return nil
}

func validAccountFingerprint(fingerprint string) bool {
	if !strings.HasPrefix(fingerprint, "SHA256:") {
		return false
	}
	encoded := strings.TrimPrefix(fingerprint, "SHA256:")
	decoded, err := base64.RawStdEncoding.DecodeString(encoded)
	return err == nil && len(decoded) == 32 && base64.RawStdEncoding.EncodeToString(decoded) == encoded
}

// Check this at compile time without invoking the host's account tools.
var _ AccountSystem = (*nativeAccountSystem)(nil)
