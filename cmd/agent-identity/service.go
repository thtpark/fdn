package main

import (
	"context"
	"flag"
	"io"
	"os"
	"syscall"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/platform"
)

type serviceLifecycle interface {
	Install(context.Context, bool) (platform.IdentityServiceStatus, error)
	Status(context.Context) (platform.IdentityServiceStatus, error)
}

type serviceFactory func(platform.IdentityServiceOptions) (serviceLifecycle, error)

func nativeServiceFactory(options platform.IdentityServiceOptions) (serviceLifecycle, error) {
	return platform.NewNativeIdentityServiceInstaller(options, nil)
}

func readServiceOptions(path string) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) || info.Size() > domain.MaxBody {
		return nil, domain.Fail(domain.Denied, "service options must be a bounded protected regular file owned by this account or root")
	}
	data, err := io.ReadAll(io.LimitReader(file, domain.MaxBody+1))
	if len(data) > domain.MaxBody {
		return nil, domain.Fail(domain.Invalid, "service options exceed their size limit")
	}
	return data, err
}

func runService(ctx context.Context, args []string, output io.Writer, factory serviceFactory) int {
	if len(args) == 0 {
		return result(output, nil, domain.Fail(domain.Invalid, "service requires plan, install or status; see --help"))
	}
	command := args[0]
	if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		_, _ = io.WriteString(output, help)
		return 0
	}
	if command != "plan" && command != "install" && command != "status" {
		return result(output, nil, domain.Fail(domain.Invalid, "unknown native identity service lifecycle command"))
	}
	flags := flag.NewFlagSet("service "+command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	input := flags.String("input", "", "protected public identity-service installation options")
	apply := false
	if command == "install" {
		flags.BoolVar(&apply, "apply", false, "explicit native administrator apply")
	}
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || *input == "" || *input == "-" {
		return result(output, nil, domain.Fail(domain.Invalid, "service lifecycle requires --input OPTIONS and no extra arguments; install also requires --apply"))
	}
	if command == "install" && !apply {
		return result(output, nil, domain.Fail(domain.Denied, "service install requires explicit --apply; use service plan for read-only preparation"))
	}
	data, err := readServiceOptions(*input)
	if err != nil {
		return result(output, nil, err)
	}
	var options platform.IdentityServiceOptions
	if err := config.Decode(data, &options); err != nil {
		return result(output, nil, err)
	}
	plan, err := platform.RenderIdentityServices(options)
	if err != nil {
		return result(output, nil, err)
	}
	if command == "plan" {
		return result(output, plan, nil)
	}
	installer, err := factory(options)
	if err != nil {
		return result(output, nil, err)
	}
	var status platform.IdentityServiceStatus
	if command == "install" {
		status, err = installer.Install(ctx, apply)
	} else {
		status, err = installer.Status(ctx)
	}
	return result(output, status, err)
}
