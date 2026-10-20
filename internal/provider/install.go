package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/process"
	"github.com/thtpark/fdn/internal/secure"
)

type Installed struct {
	Provider           string `json:"provider"`
	Version            string `json:"version"`
	Path               string `json:"path"`
	SignaturesVerified bool   `json:"registry_signatures_verified"`
	UpdatedAt          int64  `json:"updated_at"`
}

func packageName(name string) (string, error) {
	switch name {
	case "codex":
		return "@openai/codex", nil
	case "copilot":
		return "@github/copilot", nil
	default:
		return "", domain.Fail(domain.Invalid, "unsupported provider")
	}
}

func Latest(ctx context.Context, name string, client *http.Client) (string, error) {
	pkg, err := packageName(name)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, "GET", "https://registry.npmjs.org/"+url.PathEscape(pkg)+"/latest", nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", domain.Fail(domain.Unavailable, "provider registry metadata unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", domain.Fail(domain.Unavailable, "provider registry rejected the metadata request")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, domain.MaxBody+1))
	if err != nil {
		return "", err
	}
	if len(data) > domain.MaxBody {
		return "", domain.Fail(domain.Provider, "registry metadata exceeds its size limit")
	}
	var result struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", err
	}
	if result.Name != pkg || versionPattern.FindString(result.Version) != result.Version {
		return "", domain.Fail(domain.Provider, "invalid registry package identity/version")
	}
	return result.Version, nil
}

func (m *Manager) Install(ctx context.Context, name, version string) (installed Installed, err error) {
	pkg, err := packageName(name)
	if err != nil {
		return installed, err
	}
	if versionPattern.FindString(version) != version || version == "" {
		return installed, domain.Fail(domain.Invalid, "an exact semantic provider version is required")
	}
	slot, err := secure.Acquire(filepath.Join(m.Config.StateDir, "work.lock"))
	if err != nil {
		return installed, err
	}
	defer func() { err = errors.Join(err, slot.Close()) }()
	root := filepath.Join(m.Config.StateDir, "providers", name)
	if err := secure.Dir(filepath.Join(root, "versions")); err != nil {
		return installed, err
	}
	stage, err := os.MkdirTemp(filepath.Join(root, "versions"), ".stage-")
	if err != nil {
		return installed, err
	}
	published := false
	defer func() {
		if !published {
			err = errors.Join(err, os.RemoveAll(stage))
		}
	}()
	if err := secure.Create(filepath.Join(stage, "package.json"), []byte(`{"name":"fdn-provider","version":"0.0.0","private":true}`)); err != nil {
		return installed, err
	}
	for _, name := range []string{"user.npmrc", "global.npmrc"} {
		if err := secure.Create(filepath.Join(stage, name), nil); err != nil {
			return installed, err
		}
	}
	env := process.Environment("NPM_CONFIG_USERCONFIG="+filepath.Join(stage, "user.npmrc"),
		"NPM_CONFIG_GLOBALCONFIG="+filepath.Join(stage, "global.npmrc"), "NPM_CONFIG_CACHE="+filepath.Join(root, "cache"), "NPM_CONFIG_REGISTRY=https://registry.npmjs.org/")
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for _, args := range [][]string{
		{"install", "--package-lock-only", "--ignore-scripts", "--save-exact", "--no-audit", "--no-fund", pkg + "@" + version},
		{"ci", "--ignore-scripts", "--omit=dev", "--no-audit", "--no-fund"},
		{"audit", "signatures", "--omit=dev"},
	} {
		output := process.NewBuffer(64 << 10)
		runErr := m.Runner.Run(ctx, process.Spec{Path: "npm", Args: args, Dir: stage, Env: env, Stdout: output, Stderr: output})
		data, outputErr := output.Bytes()
		if err := secure.Write(filepath.Join(root, "last-install.log"), data); err != nil {
			return installed, err
		}
		if err := errors.Join(runErr, outputErr); err != nil {
			return installed, domain.Fail(domain.Provider, "provider staging/signature verification failed; previous active version is unchanged")
		}
	}
	lock, err := os.ReadFile(filepath.Join(stage, "package-lock.json"))
	if err != nil {
		return installed, err
	}
	if len(lock) > 16<<20 {
		return installed, domain.Fail(domain.Provider, "provider lockfile exceeds its size limit")
	}
	var metadata struct {
		Packages map[string]struct {
			Version   string `json:"version"`
			Integrity string `json:"integrity"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(lock, &metadata); err != nil {
		return installed, err
	}
	dependency, ok := metadata.Packages["node_modules/"+pkg]
	if !ok || dependency.Version != version || !strings.HasPrefix(dependency.Integrity, "sha512-") {
		return installed, domain.Fail(domain.Provider, "installed package does not match its exact integrity-locked version")
	}
	stagedPath := filepath.Join(stage, "node_modules", ".bin", name)
	helpCtx, stop := context.WithTimeout(ctx, 15*time.Second)
	helpErr := m.compatibility(helpCtx, name, stagedPath)
	versionOutput, versionErr := m.capture(helpCtx, stagedPath, "", "--version")
	stop()
	match := versionPattern.FindSubmatch(versionOutput)
	if helpErr != nil || versionErr != nil || len(match) != 2 || string(match[1]) != version {
		return installed, domain.Fail(domain.Provider, "staged provider did not pass version/non-prompting capability checks")
	}
	final := filepath.Join(root, "versions", version+"-"+domain.NewID("build"))
	if err := os.Rename(stage, final); err != nil {
		return installed, err
	}
	published = true
	installed = Installed{Provider: name, Version: version, Path: filepath.Join(final, "node_modules", ".bin", name), SignaturesVerified: true, UpdatedAt: time.Now().UTC().Unix()}
	if err := config.Write(filepath.Join(root, "active.json"), installed); err != nil {
		return installed, err
	}
	return installed, nil
}

func (m *Manager) MaybeUpdate(ctx context.Context) error {
	if !m.Config.AutomaticProviderUpdates {
		return nil
	}
	for _, name := range []string{"codex", "copilot"} {
		if m.Config.ProviderPaths[name] != "" {
			continue
		}
		root := filepath.Join(m.Config.StateDir, "providers", name)
		var installed Installed
		err := config.Read(filepath.Join(root, "active.json"), &installed)
		if err == nil && time.Since(time.Unix(installed.UpdatedAt, 0)) < 24*time.Hour {
			continue
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		version, queryErr := Latest(queryCtx, name, &http.Client{Timeout: 15 * time.Second})
		cancel()
		if queryErr != nil {
			return queryErr
		}
		if err == nil && installed.Version == version {
			installed.UpdatedAt = time.Now().UTC().Unix()
			if err := config.Write(filepath.Join(root, "active.json"), installed); err != nil {
				return err
			}
			continue
		}
		if _, err := m.Install(ctx, name, version); err != nil {
			return err
		}
	}
	return nil
}
