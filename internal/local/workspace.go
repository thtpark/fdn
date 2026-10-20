package local

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
)

type Binding struct {
	Ref            string `json:"ref"`
	Path           string `json:"path"`
	Device         uint64 `json:"device"`
	Inode          uint64 `json:"inode"`
	Enabled        bool   `json:"enabled"`
	AllowTools     bool   `json:"allow_tools"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func contains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && (rel == "." || (rel != ".." && !filepath.IsAbs(rel) && len(rel) > 0 && rel[:min(len(rel), 3)] != "../"))
}

func checkDirectory(path string) (string, *syscall.Stat_t, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", nil, err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return "", nil, domain.Fail(domain.Denied, "workspace must be a real owned directory without group/other write access")
	}
	if err := secure.CheckOwner(info); err != nil {
		return "", nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", nil, domain.Fail(domain.Internal, "filesystem identity unavailable")
	}
	return canonical, stat, nil
}

func (s *State) BindWorkspace(ctx context.Context, path string, allowTools bool, timeout int, forbidden []string) (Binding, error) {
	var b Binding
	if !allowTools || timeout < 1 || timeout > 86400 {
		return b, domain.Fail(domain.Prerequisite, "explicit local tool authorization and a bounded timeout are required")
	}
	canonical, stat, err := checkDirectory(path)
	if err != nil {
		return b, err
	}
	forbidden = append(append([]string{}, forbidden...), s.Root)
	for _, private := range forbidden {
		absolute, err := filepath.Abs(private)
		if err != nil {
			return b, err
		}
		if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
			absolute = resolved
		} else if !os.IsNotExist(err) {
			return b, err
		}
		if contains(canonical, absolute) || contains(absolute, canonical) {
			return b, domain.Fail(domain.Denied, "workspace must not contain or be inside private Foundation state/configuration")
		}
	}
	existing, err := s.Bindings(ctx)
	if err != nil {
		return b, err
	}
	for _, item := range existing {
		if item.Path == canonical {
			if !item.Enabled {
				return b, domain.Fail(domain.Prerequisite, "binding is disabled; explicitly re-enable it rather than bypassing the decision")
			}
			if item.Device != uint64(stat.Dev) || item.Inode != stat.Ino {
				return b, domain.Fail(domain.Conflict, "workspace identity changed; preserve the old binding and bind the replacement explicitly")
			}
			return item, nil
		}
	}
	b = Binding{Ref: domain.NewID("workspace"), Path: canonical, Device: uint64(stat.Dev), Inode: stat.Ino, Enabled: true, AllowTools: true, TimeoutSeconds: timeout}
	data, err := json.Marshal(b)
	if err != nil {
		return b, err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO bindings(id,data) VALUES(?,?)", b.Ref, data)
	return b, err
}

func (s *State) Bindings(ctx context.Context) ([]Binding, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM bindings ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Binding{}
	for rows.Next() {
		var data []byte
		var b Binding
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *State) Workspace(ctx context.Context, ref string) (Binding, error) {
	items, err := s.Bindings(ctx)
	if err != nil {
		return Binding{}, err
	}
	for _, b := range items {
		if b.Ref == ref {
			if !b.Enabled || !b.AllowTools {
				return b, domain.Fail(domain.Prerequisite, "workspace tools are disabled")
			}
			_, stat, err := checkDirectory(b.Path)
			if err != nil {
				return b, err
			}
			if uint64(stat.Dev) != b.Device || stat.Ino != b.Inode {
				return b, domain.Fail(domain.Conflict, "workspace inode changed; explicit rebinding is required")
			}
			return b, nil
		}
	}
	return Binding{}, domain.Fail(domain.NotFound, "private workspace binding not found")
}

func (s *State) EnableWorkspace(ctx context.Context, ref string, enabled bool) error {
	items, err := s.Bindings(ctx)
	if err != nil {
		return err
	}
	for _, b := range items {
		if b.Ref == ref {
			b.Enabled = enabled
			data, err := json.Marshal(b)
			if err != nil {
				return err
			}
			_, err = s.db.ExecContext(ctx, "UPDATE bindings SET data = ? WHERE id = ?", data, ref)
			return err
		}
	}
	return domain.Fail(domain.NotFound, "private workspace binding not found")
}
