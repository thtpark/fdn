package software

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
)

func useServiceFixture(fixture *softwareFixture, osName string) {
	native := fixture.Native
	native.Policy.Method, native.Policy.Kind = "user-service", "service"
	native.Policy.Platform = osName + "-" + currentHost().Arch
	native.Policy.Service, native.Policy.Executable = "fixture-tool.service", "bin/tool"
	native.Policy.Args = []string{"--data", filepath.Join(fixture.Root, "data"), "literal $value %value"}
	native.Policy.Manager = "systemd-user"
	if osName == "darwin" {
		native.Policy.Manager, native.Policy.Service = "launchd", "org.fixture.tool"
	}
	native.host = func() hostIdentity { return hostIdentity{OS: osName, Arch: currentHost().Arch, EUID: os.Geteuid()} }
	native.requirements = func(context.Context, Policy) error { return nil }
	native.servicePath = func(policy Policy) string { return filepath.Join(fixture.Root, "service", policy.Service) }
}

func (r *nativeRecorder) effect(name string) error {
	r.effects = append(r.effects, name)
	if r.failEffect == name {
		return errors.New("fixture native effect response lost")
	}
	return nil
}

func (r *nativeRecorder) systemd(spec process.Spec) error {
	args := spec.Args
	if len(args) > 0 && args[0] == "--user" {
		args = args[1:]
	}
	if len(args) == 0 {
		return errors.New("missing fixed systemd action")
	}
	if args[0] == "show" {
		state, active, fragment, unitState := "not-found", "inactive", "", ""
		pid := 0
		if r.loaded {
			state, fragment, unitState = "loaded", r.fixture.Native.serviceFilename(r.fixture.Native.Policy), "disabled"
			if r.running {
				active, pid = "active", r.pid
			}
			if r.enabled {
				unitState = "enabled"
			}
		}
		_, err := fmt.Fprintf(spec.Stdout, "LoadState=%s\nActiveState=%s\nMainPID=%d\nFragmentPath=%s\nDropInPaths=%s\nUnitFileState=%s\n",
			state, active, pid, fragment, r.dropIns, unitState)
		return err
	}
	switch args[0] {
	case "daemon-reload":
		r.loaded = true
	case "enable":
		r.enabled = true
	case "stop":
		r.running, r.pid = false, 0
	case "start":
		r.loaded, r.running = true, true
		r.pid, r.activeRoot = 900+len(r.effects), filepath.Join(r.fixture.Native.Policy.Prefix, "current")
	default:
		return errors.New("unapproved systemd action")
	}
	return r.effect(args[0])
}

func (r *nativeRecorder) launchd(spec process.Spec) error {
	if len(spec.Args) == 0 {
		return errors.New("missing fixed launchd action")
	}
	switch spec.Args[0] {
	case "list":
		if _, err := io.WriteString(spec.Stdout, "PID\tStatus\tLabel\n"); err != nil {
			return err
		}
		if r.loaded {
			pid := "-"
			if r.running {
				pid = strconv.Itoa(r.pid)
			}
			_, err := fmt.Fprintf(spec.Stdout, "%s\t0\t%s\n", pid, r.fixture.Native.Policy.Service)
			return err
		}
		return nil
	case "print":
		_, err := fmt.Fprintf(spec.Stdout, "job = {\n path = %s\n program = %s\n pid = %d\n}\n",
			r.fixture.Native.serviceFilename(r.fixture.Native.Policy),
			filepath.Join(r.activeRoot, r.fixture.Native.Policy.Executable), r.pid)
		return err
	case "bootout":
		r.loaded, r.running, r.pid = false, false, 0
		return r.effect("stop")
	case "bootstrap":
		r.loaded, r.running = true, true
		r.pid, r.activeRoot = 900+len(r.effects), filepath.Join(r.fixture.Native.Policy.Prefix, "current")
		return r.effect("start")
	default:
		return errors.New("unapproved launchd action")
	}
}

type debFixture struct {
	Version  string
	Control  []byte
	Data     []byte
	Contents map[string][]byte
	Payloads []domain.SoftwarePayload
}

type fakeExit int

func (e fakeExit) Error() string { return "fixture process exit" }
func (e fakeExit) ExitCode() int { return int(e) }

func useDebianFixture(fixture *softwareFixture) {
	native := fixture.Native
	native.Policy.Method, native.Policy.Manager = "debian", "dpkg"
	native.Policy.Platform = "linux-" + currentHost().Arch
	native.Policy.Package, native.Policy.PackageFile = "fixture-tool", "fixture-tool.deb"
	native.Policy.PackageRoot = filepath.Join(fixture.Root, "package")
	native.Policy.AllowPrivileged, native.Policy.privilegedVerified = true, true
	native.host = func() hostIdentity {
		return hostIdentity{OS: "linux", Arch: currentHost().Arch, EUID: 0, fixtureRoot: fixture.Root}
	}
	native.requirements = func(context.Context, Policy) error { return nil }
	fixture.Recorder.packages = make(map[string]debFixture)
}

func (f *softwareFixture) debianRelease(t *testing.T, revision int64) domain.SoftwareRelease {
	t.Helper()
	version := "1.0." + strconv.FormatInt(revision, 10)
	marker := "synthetic-source-bound-debian:" + version
	root := filepath.Join(f.Root, domain.NewID("debian-source"))
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, f.Native.Policy.PackageFile), []byte(marker), 0644)
	var bundle bytes.Buffer
	payloads, err := CreateBundle(&bundle, root, []domain.BuildOutput{{Path: f.Native.Policy.PackageFile, Mode: 0644}}, f.Now.Unix())
	if err != nil {
		t.Fatal(err)
	}
	releaseID := "release_debian_" + strconv.FormatInt(revision, 10)
	artifactID := "artifact_debian_" + strconv.FormatInt(revision, 10)
	manifest := domain.SoftwareManifest{FormatVersion: 1, InstanceID: f.Native.Policy.InstanceID,
		ReleaseID: releaseID, ProductID: f.Native.Policy.ProductID, ProjectID: "project_fixture", Kind: f.Native.Policy.Kind,
		Version: version, Revision: revision, CreatedAt: f.Now.Unix(),
		Source: domain.TaskSource{RepositoryID: "repository_fixture", Commit: strings.Repeat("a", 40)},
		Platforms: []domain.SoftwarePlatform{{Platform: f.Native.Policy.Platform, ArtifactID: artifactID,
			Profile: "debian", SHA256: domain.Hash(bundle.String()), Size: int64(bundle.Len()), Payloads: payloads}}}
	release := domain.SoftwareRelease{ID: releaseID, Manifest: manifest}
	f.signRelease(t, &release)
	f.Artifacts[artifactID] = append([]byte{}, bundle.Bytes()...)
	control := []byte("Package: " + f.Native.Policy.Package + "\nVersion: " + version +
		"\nArchitecture: " + currentHost().Arch + "\nDescription: Fixture package\n")
	contents := map[string][]byte{"bin/tool": []byte("Debian tool " + version), "share/notice": []byte("notice")}
	innerPayloads := []domain.SoftwarePayload{}
	headers := []*tar.Header{}
	bodies := [][]byte{}
	for _, name := range []string{"bin/tool", "share/notice"} {
		mode := uint32(0644)
		if name == "bin/tool" {
			mode = 0755
		}
		body := contents[name]
		innerPayloads = append(innerPayloads, domain.SoftwarePayload{Path: name, Mode: mode, Size: int64(len(body)), SHA256: domain.Hash(string(body))})
		headers = append(headers, &tar.Header{Name: "./" + filepath.ToSlash(strings.TrimPrefix(f.Native.Policy.PackageRoot, "/")) + "/" + name,
			Mode: int64(mode), Size: int64(len(body)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR})
		bodies = append(bodies, body)
	}
	f.Recorder.packages[marker] = debFixture{Version: version,
		Control: writeRawTar(t, []*tar.Header{{Name: "./control", Mode: 0644, Size: int64(len(control)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}}, [][]byte{control}),
		Data:    writeRawTar(t, headers, bodies), Contents: contents, Payloads: innerPayloads}
	return release
}

func (r *nativeRecorder) debian(spec process.Spec) error {
	policy := r.fixture.Native.Policy
	if spec.Path == "/usr/bin/dpkg-query" {
		switch spec.Args[0] {
		case "--show":
			if !r.packageState.Present {
				return fakeExit(1)
			}
			_, err := fmt.Fprintf(spec.Stdout, "%s\t%s\t%s\tinstall ok installed\n", policy.Package, r.packageState.Version, currentHost().Arch)
			return err
		case "--listfiles":
			_, err := fmt.Fprintf(spec.Stdout, "%s\n%s\n%s\n", policy.PackageRoot,
				filepath.Join(policy.PackageRoot, "bin/tool"), filepath.Join(policy.PackageRoot, "share/notice"))
			return err
		}
	}
	if spec.Path == "/usr/bin/dpkg-deb" {
		data, err := os.ReadFile(spec.Args[1])
		if err != nil {
			return err
		}
		fixture, ok := r.packages[string(data)]
		if !ok {
			return errors.New("unknown source-bound fixture package")
		}
		switch spec.Args[0] {
		case "--ctrl-tarfile":
			_, err = spec.Stdout.Write(fixture.Control)
		case "--fsys-tarfile":
			_, err = spec.Stdout.Write(fixture.Data)
		default:
			return errors.New("unapproved dpkg-deb action")
		}
		return err
	}
	if spec.Path == "/usr/bin/dpkg" {
		if len(spec.Args) != 3 || spec.Args[0] != "--no-triggers" || spec.Args[1] != "--install" {
			return errors.New("unapproved privileged package-manager arguments")
		}
		data, err := os.ReadFile(spec.Args[2])
		if err != nil {
			return err
		}
		fixture, found := r.packages[string(data)]
		if !found {
			return errors.New("unknown fixture package installation")
		}
		for _, payload := range fixture.Payloads {
			filename := filepath.Join(policy.PackageRoot, filepath.FromSlash(payload.Path))
			if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
				return err
			}
			if err := os.WriteFile(filename, fixture.Contents[payload.Path], os.FileMode(payload.Mode)); err != nil {
				return err
			}
		}
		r.packageState = packageState{Present: true, Version: fixture.Version}
		return r.effect("dpkg-install")
	}
	return errors.New("unapproved native executable")
}

func TestNativeUserServicesFixedLifecycleAndReplay(t *testing.T) {
	for _, osName := range []string{"linux", "darwin"} {
		t.Run(osName, func(t *testing.T) {
			fixture := nativeFixture(t)
			useServiceFixture(fixture, osName)
			first := fixture.release(t, 1)
			permit := fixture.permit(t, "service-first", first)
			prepared, err := fixture.Native.Handle(context.Background(), domain.SoftwareNativeRequest{
				Action: "prepare", ID: permit.Proposal.Challenge.ID, Permit: &permit})
			if err != nil || prepared.State != "prepared" || len(fixture.Recorder.effects) != 0 || fixture.Leases != 0 {
				t.Fatalf("prepare invoked a service or lease: %#v %v", prepared, err)
			}
			applyPermit(t, fixture.Native, permit)
			if !fixture.Recorder.running {
				t.Fatal("first service install was not started")
			}
			second := fixture.release(t, 2)
			upgradePermit := fixture.permit(t, "service-upgrade", second)
			applyPermit(t, fixture.Native, upgradePermit)
			if !fixture.Recorder.running || fixture.Leases != 2 {
				t.Fatal("service upgrade did not stop, install and restore its intended running state")
			}
			before := len(fixture.Recorder.calls)
			for _, action := range []string{"status", "apply", "prepare"} {
				request := domain.SoftwareNativeRequest{Action: action, ID: upgradePermit.Proposal.Challenge.ID}
				if action != "status" {
					request.Permit = &upgradePermit
				}
				reply, err := fixture.Native.Handle(context.Background(), request)
				if err != nil || reply.State != "installed" || reply.Result.ResumeSafe {
					t.Fatalf("exact replay: %#v %v", reply, err)
				}
			}
			if len(fixture.Recorder.calls) != before || fixture.Leases != 2 || fixture.Fetches != 2 {
				t.Fatal("receipt replay invoked a native manager, fetch or renewed lease")
			}
			for _, call := range fixture.Recorder.calls {
				if call.Path != "/usr/bin/systemctl" && call.Path != "/bin/launchctl" {
					t.Fatal("service profile admitted a generic command", call.Path)
				}
			}
		})
	}
}

func TestNativeServiceUncertaintyRetainsFenceAndNeverReruns(t *testing.T) {
	for _, effect := range []string{"start", "stop"} {
		t.Run(effect, func(t *testing.T) {
			fixture := nativeFixture(t)
			useServiceFixture(fixture, "linux")
			first := fixture.release(t, 1)
			permit := fixture.permit(t, "uncertain-first", first)
			if effect == "stop" {
				applyPermit(t, fixture.Native, permit)
				permit = fixture.permit(t, "uncertain-upgrade", fixture.release(t, 2))
			}
			fixture.Recorder.failEffect = effect
			reply, err := fixture.Native.Handle(context.Background(), domain.SoftwareNativeRequest{
				Action: "apply", ID: permit.Proposal.Challenge.ID, Permit: &permit})
			if err == nil || reply.State != "operator-required" || reply.Result == nil || reply.Result.Code != map[string]string{"start": "starting", "stop": "stopping"}[effect] {
				t.Fatalf("unknown effects were not fenced: %#v %v", reply, err)
			}
			calls, leases, fetches := len(fixture.Recorder.calls), fixture.Leases, fixture.Fetches
			fixture.Now = fixture.Now.Add(2 * time.Hour)
			for _, action := range []string{"status", "apply", "prepare"} {
				request := domain.SoftwareNativeRequest{Action: action, ID: permit.Proposal.Challenge.ID}
				if action != "status" {
					request.Permit = &permit
				}
				replayed, err := fixture.Native.Handle(context.Background(), request)
				if err == nil || replayed.State != "operator-required" || !equalJSON(replayed.Result, reply.Result) {
					t.Fatalf("uncertainty replay changed: %#v %v", replayed, err)
				}
			}
			if len(fixture.Recorder.calls) != calls || fixture.Leases != leases || fixture.Fetches != fetches {
				t.Fatal("uncertain effect was rerun")
			}
			if _, err := fixture.Native.Handle(context.Background(), domain.SoftwareNativeRequest{Action: "challenge", ID: "cannot-take-over"}); err == nil {
				t.Fatal("unresolved native ownership was taken over")
			}
		})
	}
}

func TestNativeDebianNewUpgradeAdoptionAndNoRootHooks(t *testing.T) {
	fixture := nativeFixture(t)
	useDebianFixture(fixture)
	first := fixture.debianRelease(t, 1)
	permit := fixture.permit(t, "debian-first", first)
	applyPermit(t, fixture.Native, permit)
	old, _, err := fixture.Native.readReceipt(permit.Proposal.Challenge.ID)
	if err != nil {
		t.Fatal(err)
	}
	second := fixture.debianRelease(t, 2)
	upgradePermit := fixture.permit(t, "debian-upgrade", second)
	applyPermit(t, fixture.Native, upgradePermit)
	if err := verifyTree(old.VersionDir, old.Payloads, true); err != nil {
		t.Fatal("Debian manager overwrote the old immutable side-by-side version", err)
	}
	calls := len(fixture.Recorder.calls)
	applyPermit(t, fixture.Native, upgradePermit)
	if len(fixture.Recorder.calls) != calls || len(fixture.Recorder.effects) != 2 {
		t.Fatal("Debian receipt replay reran the package manager")
	}
	adoption := nativeFixture(t)
	useDebianFixture(adoption)
	baseline := adoption.debianRelease(t, 1)
	marker := "synthetic-source-bound-debian:" + baseline.Manifest.Version
	deb := adoption.Recorder.packages[marker]
	for _, payload := range deb.Payloads {
		writeTestFile(t, filepath.Join(adoption.Native.Policy.PackageRoot, payload.Path), deb.Contents[payload.Path], os.FileMode(payload.Mode))
	}
	baselinePackage := filepath.Join(adoption.Root, "baseline.deb")
	writeTestFile(t, baselinePackage, []byte(marker), 0644)
	adoption.Recorder.packageState = packageState{Present: true, Version: baseline.Manifest.Version}
	adoption.Native.Policy.Adoption = &Baseline{Release: baseline, Path: adoption.Native.Policy.PackageRoot, PackageFile: baselinePackage}
	adoptPermit := adoption.permit(t, "debian-adopt", baseline)
	if adoptPermit.Proposal.Challenge.State != "adoptable" {
		t.Fatal("Debian adoption did not verify the existing baseline")
	}
	applyPermit(t, adoption.Native, adoptPermit)
}

func TestNativeDebianRejectsPrivilegeScriptsWrongPackageAndPathsBeforeEffects(t *testing.T) {
	for _, name := range []string{"privilege", "script", "package", "traversal", "symlink"} {
		t.Run(name, func(t *testing.T) {
			fixture := nativeFixture(t)
			useDebianFixture(fixture)
			release := fixture.debianRelease(t, 1)
			if name == "privilege" {
				fixture.Native.Policy.privilegedVerified = false
				if _, err := fixture.Native.Handle(context.Background(), domain.SoftwareNativeRequest{Action: "challenge", ID: "denied-debian"}); err == nil {
					t.Fatal("unprovisioned privileged policy was accepted")
				}
				requireNoPrefix(t, fixture)
				return
			}
			permit := fixture.permit(t, "unsafe-debian", release)
			marker := "synthetic-source-bound-debian:" + release.Manifest.Version
			deb := fixture.Recorder.packages[marker]
			switch name {
			case "script":
				control := []byte("Package: fixture-tool\nVersion: 1.0.1\nArchitecture: " + currentHost().Arch + "\n")
				deb.Control = writeRawTar(t, []*tar.Header{
					{Name: "./control", Mode: 0644, Size: int64(len(control)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR},
					{Name: "./postinst", Mode: 0755, Size: 2, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR},
				}, [][]byte{control, []byte("sh")})
			case "package":
				control := []byte("Package: unrelated-package\nVersion: 1.0.1\nArchitecture: " + currentHost().Arch + "\n")
				deb.Control = writeRawTar(t, []*tar.Header{{Name: "./control", Mode: 0644, Size: int64(len(control)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}}, [][]byte{control})
			case "traversal":
				deb.Data = writeRawTar(t, []*tar.Header{{Name: "../escape", Mode: 0644, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}}, nil)
			case "symlink":
				deb.Data = writeRawTar(t, []*tar.Header{{Name: "./" + strings.TrimPrefix(fixture.Native.Policy.PackageRoot, "/") + "/link",
					Mode: 0755, Typeflag: tar.TypeSymlink, Linkname: "/etc", Format: tar.FormatUSTAR}}, nil)
			}
			fixture.Recorder.packages[marker] = deb
			reply, err := fixture.Native.Handle(context.Background(), domain.SoftwareNativeRequest{
				Action: "apply", ID: permit.Proposal.Challenge.ID, Permit: &permit})
			if err == nil || reply.State != "failed" {
				t.Fatalf("unsafe Debian source was accepted: %#v %v", reply, err)
			}
			requireNoPrefix(t, fixture)
		})
	}
}
