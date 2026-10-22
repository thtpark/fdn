package provider

import (
	"context"
	"regexp"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

// LoginCommand only probes help; the authorized caller starts the login process.
func (m *Manager) LoginCommand(ctx context.Context, name string) (string, []string, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	path, err := m.Resolve(name)
	if err != nil {
		return "", nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	probe, err := NativeArguments(name, []string{"login", "--help"})
	if err != nil {
		return "", nil, err
	}
	help, err := m.capture(ctx, path, "", probe...)
	if ctx.Err() != nil {
		return "", nil, ctx.Err()
	}
	if err != nil {
		return "", nil, domain.Fail(domain.Prerequisite, "provider login capability probe failed; no sign-in was started")
	}
	flag := "--device-auth"
	if name == "copilot" {
		flag = "--device-code"
	}
	args := []string{"login"}
	pattern := regexp.MustCompile(`(?:^|[\s\[])` + regexp.QuoteMeta(flag) + `(?:$|[\s=,\]])`)
	if pattern.Match(help) {
		args = append(args, flag)
	}
	args, err = NativeArguments(name, args)
	return path, args, err
}
