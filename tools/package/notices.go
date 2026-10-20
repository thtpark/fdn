package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/thtpark/fdn/internal/domain"
)

type dependencyModule struct {
	Path    string
	Version string
	Dir     string
	Main    bool
}

func thirdPartyNotices() ([]byte, error) {
	modFile, err := command("go", "env", "GOMOD")
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("go", "list", "-deps", "-json", "./cmd/...")
	cmd.Dir = filepath.Dir(modFile)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	modules := map[string]dependencyModule{}
	decoder := json.NewDecoder(stdout)
	for {
		var pkg struct{ Module *dependencyModule }
		if err := decoder.Decode(&pkg); err != nil {
			if err == io.EOF {
				break
			}
			cmd.Wait()
			return nil, err
		}
		if pkg.Module != nil && !pkg.Module.Main {
			modules[pkg.Module.Path] = *pkg.Module
		}
	}
	if err := cmd.Wait(); err != nil {
		return nil, err
	}
	goRoot, err := command("go", "env", "GOROOT")
	if err != nil {
		return nil, err
	}
	goLicense, err := os.ReadFile(filepath.Join(goRoot, "LICENSE"))
	if os.IsNotExist(err) {
		goLicense, err = os.ReadFile(filepath.Join(goRoot, "..", "LICENSE"))
	}
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.WriteString("Third-party license notices\n\nGo runtime and standard library\n")
	out.Write(goLicense)
	names := make([]string, 0, len(modules))
	for name := range modules {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		module := modules[name]
		entries, err := os.ReadDir(module.Dir)
		if err != nil {
			return nil, err
		}
		found := false
		for _, entry := range entries {
			upper := strings.ToUpper(entry.Name())
			if entry.IsDir() || (!strings.HasPrefix(upper, "LICENSE") && !strings.HasPrefix(upper, "COPYING") && upper != "NOTICE") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(module.Dir, entry.Name()))
			if err != nil {
				return nil, err
			}
			if len(data) > 1<<20 {
				return nil, domain.Fail(domain.Invalid, "dependency license exceeds its notice size limit")
			}
			fmt.Fprintf(&out, "\n\n%s %s (%s)\nSource module: https://%s\n\n", module.Path, module.Version, entry.Name(), module.Path)
			out.Write(data)
			found = true
		}
		if !found {
			return nil, domain.Fail(domain.Prerequisite, "dependency has no root license/notice file: "+module.Path)
		}
	}
	return out.Bytes(), nil
}
