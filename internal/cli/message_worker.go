package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
	"github.com/thtpark/fdn/internal/secure"
	"github.com/thtpark/fdn/internal/upgrade"
)

func (a *app) messageWorker(ctx context.Context, r *resources, args []string) (any, error) {
	a.nativeOutput = true
	if err := requireArgs(args, 0); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(a.in, domain.MaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > domain.MaxBody {
		return nil, domain.Fail(domain.Invalid, "maintenance mailbox request exceeds its size bound")
	}
	var request domain.MessageWorkerCommand
	if err := message.UnmarshalPayload(raw, &request); err != nil {
		return nil, err
	}
	reply := domain.MessageWorkerReply{Messages: []domain.Message{}}
	var actionErr error
	if request.Report != nil {
		var updated domain.Message
		updated, actionErr = api.Call[domain.Message](ctx, r.client, "POST", api.Path("messages", request.MessageID, "request"), *request.Report)
		if actionErr == nil {
			reply.Message = &updated
		}
	} else {
		reply.Messages, actionErr = api.Call[[]domain.Message](ctx, r.client, "GET", "/v1/message-requests", nil)
	}
	if actionErr != nil {
		reply.Error = domain.AsFault(actionErr)
	}
	data, err := message.MarshalPayload(reply)
	if err != nil {
		return nil, err
	}
	_, writeErr := a.out.Write(data)
	return nil, errors.Join(actionErr, writeErr)
}

func (a *app) requestWorker(ctx context.Context, args []string) (any, error) {
	flags := flag.NewFlagSet("request-worker", flag.ContinueOnError)
	path := flags.String("policy", "", "separately provisioned native maintenance policy")
	once := flags.Bool("once", false, "inspect at most one queued native request")
	if err := parse(flags, args); err != nil {
		return nil, err
	}
	if *path == "" {
		return nil, domain.Fail(domain.Invalid, "request-worker requires an explicit separately provisioned --policy")
	}
	policy, err := upgrade.ReadPolicy(*path)
	if err != nil {
		return nil, err
	}
	if data, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		if strings.Contains(string(data), "fdn-worker-") || strings.Contains(string(data), "fdn-identity-") || strings.Contains(string(data), "fdn-foundation.service") || strings.Contains(string(data), "fdn-accounts.service") {
			return nil, domain.Fail(domain.Denied, "native request worker must be supervised independently of the services it stops")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := secure.Dir(policy.StateDir); err != nil {
		return nil, err
	}
	lock, err := secure.Acquire(filepath.Join(policy.StateDir, "request-worker.lock"))
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	native := upgrade.NewNative(policy)
	for {
		handled, err := native.MessageStep(ctx)
		if *once {
			return map[string]bool{"request_processed": handled}, err
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if _, writeErr := fmt.Fprintln(a.diagnostic, domain.AsFault(err)); writeErr != nil {
				return nil, errors.Join(err, writeErr)
			}
		}
		wait := 30 * time.Second
		if handled && err == nil {
			wait = time.Second
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}
