package software

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"filippo.io/age"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

// Baseline is explicit enrollment evidence, not permission to discover or
// silently adopt software. Debian adoption additionally pins a local signed .deb.
type Baseline struct {
	Release       domain.SoftwareRelease `json:"release"`
	Path          string                 `json:"path"`
	PackageFile   string                 `json:"package_file,omitempty"`
	ReceiptFile   string                 `json:"receipt_file,omitempty"`
	ReceiptSHA256 string                 `json:"receipt_sha256,omitempty"`
}

type DependencyPolicy struct {
	Reference     domain.SoftwareDependency `json:"reference"`
	Prefix        string                    `json:"prefix"`
	ReceiptFile   string                    `json:"receipt_file"`
	ReceiptSHA256 string                    `json:"receipt_sha256"`
	PolicySHA256  string                    `json:"policy_sha256"`
}

// Policy is a provisioned, one-product native capability. No caller-supplied
// command, hook, user, destination, service or package is admitted by Handle.
type Policy struct {
	FormatVersion   int                `json:"format_version"`
	InstanceID      string             `json:"instance_id"`
	TargetID        string             `json:"target_id"`
	BindingID       string             `json:"binding_id"`
	Generation      int64              `json:"generation"`
	ProductID       string             `json:"product_id"`
	Kind            string             `json:"kind"`
	Platform        string             `json:"platform"`
	User            string             `json:"user"`
	UID             int                `json:"uid"`
	Home            string             `json:"home"`
	PublicKeyFile   string             `json:"public_key_file"`
	CAFile          string             `json:"ca_file"`
	StateDir        string             `json:"state_dir"`
	Prefix          string             `json:"prefix"`
	CurrentReceipt  string             `json:"current_receipt"`
	Method          string             `json:"method"`
	Manager         string             `json:"manager"`
	Service         string             `json:"service,omitempty"`
	Executable      string             `json:"executable,omitempty"`
	Args            []string           `json:"args,omitempty"`
	Package         string             `json:"package,omitempty"`
	PackageFile     string             `json:"package_file,omitempty"`
	PackageRoot     string             `json:"package_root,omitempty"`
	AllowPrivileged bool               `json:"allow_privileged,omitempty"`
	BackupPaths     []string           `json:"backup_paths"`
	BackupRecipient string             `json:"backup_recipient"`
	Adoption        *Baseline          `json:"adoption,omitempty"`
	Current         *Baseline          `json:"current,omitempty"`
	Dependencies    []DependencyPolicy `json:"dependencies,omitempty"`

	privilegedVerified bool
	sourceFile         string
	sourceSHA256       string
}

type hostIdentity struct {
	OS, Arch    string
	EUID        int
	fixtureRoot string
}

func currentHost() hostIdentity {
	return hostIdentity{OS: runtime.GOOS, Arch: runtime.GOARCH, EUID: os.Geteuid()}
}

func normalizePolicy(policy Policy) Policy {
	if policy.FormatVersion == 0 {
		policy.FormatVersion = 1
	}
	if policy.CurrentReceipt == "" {
		policy.CurrentReceipt = filepath.Join(policy.StateDir, "current.json")
	}
	if policy.Method == "archive" && policy.Manager == "" {
		policy.Manager = "none"
	}
	return policy
}

func inside(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func overlap(a, b string) bool { return inside(a, b) || inside(b, a) }

func absolutePath(value string) bool {
	if !utf8.ValidString(value) || !filepath.IsAbs(value) || filepath.Clean(value) != value || value == "/" || len(value) > 2048 {
		return false
	}
	for _, r := range value {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

func protectedPath(value string, owner uint32, allowMissing bool) error {
	if !absolutePath(value) {
		return domain.Fail(domain.Invalid, "native software paths must be clean absolute non-root paths")
	}
	for current := value; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && allowMissing {
			continue
		}
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (stat.Uid != 0 && stat.Uid != owner) || info.Mode()&os.ModeSymlink != 0 ||
			info.Mode().Perm()&0022 != 0 || (current != value && !info.IsDir()) {
			return domain.Fail(domain.Denied, "native software requires protected real paths owned by the installing account or root")
		}
		if current == filepath.Dir(current) {
			return nil
		}
	}
}

var (
	servicePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	packagePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]{1,127}$`)
)

func (policy Policy) serviceFile(host hostIdentity) string {
	if policy.Service == "" {
		return ""
	}
	if policy.Method == "debian" {
		return filepath.Join("/etc/systemd/system", policy.Service)
	}
	if host.OS == "linux" {
		return filepath.Join(policy.Home, ".config/systemd/user", policy.Service)
	}
	return filepath.Join(policy.Home, "Library/LaunchAgents", policy.Service+".plist")
}

func (policy Policy) validateFor(host hostIdentity) error {
	policy = normalizePolicy(policy)
	if policy.Kind == "foundation" {
		return domain.Fail(domain.Prerequisite, "Foundation native installation requires the separately provisioned existing Foundation updater bridge")
	}
	if policy.FormatVersion != 1 || !domain.ValidOperation(policy.InstanceID) ||
		!domain.ValidOperation(policy.TargetID) || !domain.ValidOperation(policy.BindingID) ||
		policy.Generation < 1 || !domain.ValidOperation(policy.ProductID) || !domain.ValidOperation(policy.Kind) ||
		!validPlatform(policy.Platform) || policy.Platform != host.OS+"-"+host.Arch ||
		!validProfile(policy.Method) || policy.User == "" || policy.User == "root" || policy.UID <= 0 {
		return domain.Fail(domain.Invalid, "software policy requires one exact existing account, enrolled computer, product and native platform")
	}
	account, err := user.Lookup(policy.User)
	if err != nil {
		return domain.Fail(domain.Prerequisite, "software installing account does not exist")
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil || uid != policy.UID || account.HomeDir != policy.Home {
		return domain.Fail(domain.Denied, "software policy does not match the actual existing installing user UID and HOME")
	}
	if policy.Method == "debian" {
		if host.OS != "linux" || host.EUID != 0 || !policy.AllowPrivileged || !policy.privilegedVerified ||
			policy.Manager != "dpkg" || !packagePattern.MatchString(policy.Package) ||
			policy.PackageFile == "" || filepath.Ext(policy.PackageFile) != ".deb" {
			return domain.Fail(domain.Denied, "Debian software requires an explicitly root-provisioned Linux policy and exact package")
		}
		if err := relativePath(policy.PackageFile); err != nil {
			return err
		}
		for _, prefix := range []string{policy.Prefix, policy.PackageRoot} {
			narrow := prefix != "/opt" && inside("/opt", prefix)
			fixture := host.fixtureRoot != "" && prefix != host.fixtureRoot && inside(host.fixtureRoot, prefix)
			if !narrow && !fixture {
				return domain.Fail(domain.Denied, "Debian native software requires isolated application/package prefixes beneath /opt, not OS configuration or package-manager roots")
			}
		}
	} else {
		if host.EUID != policy.UID || policy.AllowPrivileged || policy.Package != "" ||
			policy.PackageFile != "" || policy.PackageRoot != "" {
			return domain.Fail(domain.Denied, "portable and user-service software runs only as its existing installing user, without root capabilities")
		}
		if !inside(policy.Home, policy.Prefix) || !inside(policy.Home, policy.StateDir) ||
			policy.Prefix == policy.Home || policy.StateDir == policy.Home {
			return domain.Fail(domain.Denied, "user software storage and activation must remain within the actual installing HOME")
		}
	}
	if policy.Method == "archive" {
		if policy.Manager != "none" || policy.Service != "" || policy.Executable != "" || len(policy.Args) != 0 {
			return domain.Fail(domain.Invalid, "portable archives have no implicit process or service-control hook")
		}
	} else if policy.Method == "user-service" {
		if policy.Service == "" || host.OS == "linux" && policy.Manager != "systemd-user" ||
			host.OS == "darwin" && policy.Manager != "launchd" {
			return domain.Fail(domain.Prerequisite, "user-service software requires its fixed native systemd-user or launchd profile")
		}
	}
	if policy.Service != "" {
		if !servicePattern.MatchString(policy.Service) || strings.HasPrefix(policy.Service, "fdn-") ||
			(host.OS == "linux" && !strings.HasSuffix(policy.Service, ".service")) {
			return domain.Fail(domain.Denied, "software service policy requires a non-Foundation exact native service name")
		}
		if err := relativePath(policy.Executable); err != nil {
			return err
		}
		if len(policy.Args) > 32 {
			return domain.Fail(domain.Invalid, "software service arguments exceed their fixed policy limit")
		}
		for _, arg := range policy.Args {
			if len(arg) > 1024 || !utf8.ValidString(arg) || strings.ContainsAny(arg, "\x00\r\n") {
				return domain.Fail(domain.Invalid, "software service arguments must be bounded single-line policy literals")
			}
		}
	} else if policy.Executable != "" || len(policy.Args) != 0 {
		return domain.Fail(domain.Invalid, "software executable arguments require an explicit fixed service")
	}
	if len(policy.BackupPaths) > 16 || len(policy.Dependencies) > 64 || policy.Adoption != nil && policy.Current != nil {
		return domain.Fail(domain.Invalid, "software policy backup/dependency/baseline capabilities are not bounded or are ambiguous")
	}
	if _, err := age.ParseX25519Recipient(policy.BackupRecipient); err != nil {
		return domain.Fail(domain.Prerequisite, "software policy requires the pinned public recipient of an off-host age recovery key")
	}
	paths := []string{policy.Home, policy.PublicKeyFile, policy.CAFile, policy.StateDir, policy.Prefix, policy.CurrentReceipt}
	if policy.Method == "debian" {
		paths = append(paths, policy.PackageRoot)
		if overlap(policy.PackageRoot, policy.Prefix) || overlap(policy.PackageRoot, policy.StateDir) {
			return domain.Fail(domain.Invalid, "Debian staging ownership must be separate from immutable activation and native evidence")
		}
	}
	if policy.CurrentReceipt != filepath.Join(policy.StateDir, "current.json") || overlap(policy.StateDir, policy.Prefix) ||
		policy.PublicKeyFile == policy.CAFile {
		return domain.Fail(domain.Invalid, "software policy must pin separate trust, receipt, protected evidence and activation paths")
	}
	seenBackups := make(map[string]bool)
	for _, backup := range policy.BackupPaths {
		if seenBackups[backup] || overlap(backup, policy.StateDir) || overlap(backup, policy.Prefix) ||
			policy.Method == "debian" && overlap(backup, policy.PackageRoot) ||
			policy.Method != "debian" && !inside(policy.Home, backup) {
			return domain.Fail(domain.Invalid, "software data backups must be distinct, explicitly scoped and outside native evidence/activation")
		}
		paths = append(paths, backup)
		seenBackups[backup] = true
	}
	for _, baseline := range []*Baseline{policy.Adoption, policy.Current} {
		if baseline == nil {
			continue
		}
		if baseline.Release.Manifest.ProductID != policy.ProductID || baseline.Release.Manifest.InstanceID != policy.InstanceID ||
			baseline.Release.Manifest.Kind != policy.Kind {
			return domain.Fail(domain.Denied, "software enrollment baseline belongs to a different product or authority")
		}
		if err := ValidateManifest(baseline.Release.Manifest); err != nil {
			return err
		}
		platform, err := platformFor(baseline.Release, policy.Platform)
		if err != nil || platform.Profile != policy.Method {
			return domain.Fail(domain.Denied, "software baseline is incompatible with the exact native profile")
		}
		paths = append(paths, baseline.Path)
		if baseline == policy.Adoption {
			if overlap(baseline.Path, policy.StateDir) || overlap(baseline.Path, policy.Prefix) ||
				policy.Method != "debian" && !inside(policy.Home, baseline.Path) {
				return domain.Fail(domain.Invalid, "approved adoption must name a separate verified existing installation")
			}
			if policy.Method == "debian" {
				if baseline.Path != policy.PackageRoot || baseline.PackageFile == "" {
					return domain.Fail(domain.Prerequisite, "Debian adoption requires its exact existing package tree and signed local package bytes")
				}
				paths = append(paths, baseline.PackageFile)
			} else if baseline.PackageFile != "" {
				return domain.Fail(domain.Invalid, "portable baseline cannot add a Debian package capability")
			}
		} else {
			if baseline.Path != policy.Prefix || !domain.ValidHash(baseline.ReceiptSHA256) || baseline.ReceiptFile == "" {
				return domain.Fail(domain.Prerequisite, "managed policy enrollment requires an exact existing native receipt and activation prefix")
			}
			paths = append(paths, baseline.ReceiptFile)
		}
	}
	seenDependencies := make(map[string]bool)
	for _, dependency := range policy.Dependencies {
		if err := validateDependencies([]domain.SoftwareDependency{dependency.Reference}, policy.Platform); err != nil {
			return err
		}
		if dependency.Reference.ProductID == policy.ProductID || seenDependencies[dependency.Reference.ProductID] ||
			!domain.ValidHash(dependency.ReceiptSHA256) || !domain.ValidHash(dependency.PolicySHA256) ||
			overlap(dependency.Prefix, policy.Prefix) || overlap(dependency.Prefix, policy.StateDir) {
			return domain.Fail(domain.Invalid, "software dependencies require distinct exact protected native receipt anchors")
		}
		seenDependencies[dependency.Reference.ProductID] = true
		paths = append(paths, dependency.Prefix, dependency.ReceiptFile)
	}
	for _, value := range paths {
		if !absolutePath(value) {
			return domain.Fail(domain.Invalid, "software policy paths must be clean absolute non-root paths")
		}
	}
	return nil
}

func (policy Policy) Validate() error { return policy.validateFor(currentHost()) }

func decodeJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return domain.Fail(domain.Invalid, "native software record is not valid strict JSON")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return domain.Fail(domain.Invalid, "native software record contains trailing JSON")
	}
	return nil
}

func ReadPolicy(filename string) (Policy, error) {
	var policy Policy
	if err := protectedPath(filename, uint32(os.Geteuid()), false); err != nil {
		return policy, err
	}
	data, err := secure.ReadPublic(filename, 2*MaxManifestBytes)
	if err != nil {
		return policy, err
	}
	if err := decodeJSON(data, &policy); err != nil {
		return policy, err
	}
	policy = normalizePolicy(policy)
	policy.sourceFile, policy.sourceSHA256 = filename, domain.Hash(string(data))
	if policy.Method == "debian" {
		info, err := os.Lstat(filename)
		if err != nil {
			return policy, err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		policy.privilegedVerified = ok && stat.Uid == 0 && os.Geteuid() == 0
	}
	if err := policy.Validate(); err != nil {
		return policy, err
	}
	for _, value := range []string{policy.StateDir, policy.Prefix, policy.PublicKeyFile, policy.CAFile} {
		if err := protectedPath(value, uint32(os.Geteuid()), value == policy.Prefix); err != nil {
			return policy, err
		}
	}
	if err := secure.Dir(policy.StateDir); err != nil {
		return policy, err
	}
	return policy, nil
}
