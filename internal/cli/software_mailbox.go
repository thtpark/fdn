package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/ingress"
	"github.com/thtpark/fdn/internal/message"
	"github.com/thtpark/fdn/internal/process"
)

func softwareMailbox(ctx context.Context, client *api.Client, root bool, request domain.SoftwareMailboxRequest) (domain.SoftwareMailboxReply, error) {
	var reply domain.SoftwareMailboxReply
	operation := ""
	var input, output any
	method, path := "GET", ""
	switch request.Action {
	case "preflights":
		path, operation, output = "/v1/software-preflight-requests", "query.maintenance.software-preflights", &reply.Preflights
	case "inbox":
		path, operation, output = "/v1/software-requests", "query.maintenance.software-requests", &reply.Deployments
	case "preflight-result":
		if request.PreflightReport == nil {
			return reply, domain.Fail(domain.Invalid, "software mailbox preflight result is required")
		}
		reply.Preflight = new(domain.SoftwarePreflight)
		method, path, operation, input, output = "POST", "/v1/software-preflight-results", "command.maintenance.software-preflight-result", request.PreflightReport, reply.Preflight
	case "result":
		if request.Result == nil {
			return reply, domain.Fail(domain.Invalid, "software mailbox result is required")
		}
		reply.Deployment = new(domain.SoftwareDeployment)
		method, path, operation, input, output = "POST", "/v1/software-results", "command.maintenance.software-result", request.Result, reply.Deployment
	case "lease":
		if request.Lease == nil {
			return reply, domain.Fail(domain.Invalid, "software mailbox lease is required")
		}
		reply.Lease = new(domain.UpgradeLease)
		method, path, operation, input, output = "POST", "/v1/software-leases", "command.maintenance.software-lease", request.Lease, reply.Lease
	case "artifact":
		if !domain.ValidOperation(request.ArtifactID) {
			return reply, domain.Fail(domain.Invalid, "immutable software artifact ID is required")
		}
		reply.Artifact = new(domain.DeliveryArtifact)
		path, operation, output = api.Path("artifacts", request.ArtifactID), "query.maintenance.software-artifact", reply.Artifact
	case "chunk":
		if !domain.ValidOperation(request.ArtifactID) || request.Offset < 0 {
			return reply, domain.Fail(domain.Invalid, "software artifact chunk needs an exact ID and offset")
		}
		reply.Chunk = new(domain.FileChunk)
		path, operation, output = api.Path("artifacts", request.ArtifactID, "chunks")+"?offset="+strconv.FormatInt(request.Offset, 10),
			"query.maintenance.software-artifact-chunk", reply.Chunk
	default:
		return reply, domain.Fail(domain.Invalid, "unsupported software mailbox capability")
	}
	if (request.Action != "preflight-result" && request.PreflightReport != nil) ||
		(request.Action != "result" && request.Result != nil) || (request.Action != "lease" && request.Lease != nil) ||
		(request.Action != "artifact" && request.Action != "chunk" && request.ArtifactID != "") ||
		(request.Action != "chunk" && request.Offset != 0) {
		return reply, domain.Fail(domain.Invalid, "software mailbox fields do not match the selected narrow action")
	}
	if !root {
		err := client.Request(ctx, method, path, input, output)
		return reply, err
	}
	native := message.Request{Operation: operation}
	if input != nil {
		data, err := message.MarshalPayload(input)
		if err != nil {
			return reply, err
		}
		native.Payload = data
	}
	if request.Action == "artifact" || request.Action == "chunk" {
		native.Target = request.ArtifactID
		if request.Action == "chunk" {
			native.Query = map[string]string{"offset": strconv.FormatInt(request.Offset, 10)}
		}
	}
	data, err := client.Message(ctx, native)
	if err != nil {
		return reply, err
	}
	return reply, message.UnmarshalPayload(data, output)
}

func (a *app) softwareMailboxCommand(ctx context.Context, r *resources, args []string) (any, error) {
	a.nativeOutput = true
	if err := requireArgs(args, 0); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(a.in, domain.MaxBody+1))
	if err != nil || len(data) > domain.MaxBody {
		return nil, domain.Fail(domain.Invalid, "software mailbox input exceeds the native request bound")
	}
	var input domain.SoftwareMailboxRequest
	if err := message.UnmarshalPayload(data, &input); err != nil {
		return nil, err
	}
	reply, actionErr := softwareMailbox(ctx, r.client, false, input)
	if actionErr != nil {
		reply.Error = domain.AsFault(actionErr)
	}
	encoded, err := message.MarshalPayload(reply)
	if err != nil {
		return nil, err
	}
	_, writeErr := a.out.Write(encoded)
	return nil, errors.Join(actionErr, writeErr)
}

func (a *app) rootSoftwareClient() (*api.Client, error) {
	if os.Geteuid() != 0 {
		return nil, domain.Fail(domain.Denied, "Foundation software maintenance requires its separately provisioned root operator")
	}
	server, uid, err := readMaintenanceConfig(a.configFile)
	if err != nil {
		return nil, err
	}
	socket := filepath.Join(server.StateDir, "maintenance.sock")
	client := api.NewMessageClient("unix://maintenance.local", func(ctx context.Context) (net.Conn, error) {
		info, err := os.Lstat(socket)
		if err != nil {
			return nil, err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uid || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
			return nil, domain.Fail(domain.Denied, "Foundation software maintenance socket changed")
		}
		conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
		if err != nil {
			return nil, err
		}
		peer, ok := ingress.UID(ingress.ConnContext(ctx, conn))
		if !ok || peer != uid {
			_ = conn.Close()
			return nil, domain.Fail(domain.Denied, "Foundation maintenance is not the pinned local native peer")
		}
		return conn, nil
	})
	return client, nil
}

func privilegedSoftwareMailbox(ctx context.Context, user, binary, configFile string, request domain.SoftwareMailboxRequest) (domain.SoftwareMailboxReply, error) {
	var reply domain.SoftwareMailboxReply
	data, err := message.MarshalPayload(request)
	if err != nil {
		return reply, err
	}
	output, diagnostic := process.NewBuffer(2<<20), process.NewBuffer(64<<10)
	if err := (process.Native{}).Run(ctx, process.Spec{Path: "/usr/sbin/runuser",
		Args: []string{"-u", user, "--", binary, "--config", configFile, "--non-interactive", "--timeout", "30", "software-mailbox"},
		Env:  process.Environment(), Stdin: bytes.NewReader(data), Stdout: output, Stderr: diagnostic}); err != nil {
		return reply, errors.Join(domain.Fail(domain.Unavailable, "policy-bound installing-user software mailbox failed"), err)
	}
	raw, err := output.Bytes()
	if err != nil {
		return reply, err
	}
	if err := message.UnmarshalPayload(raw, &reply); err != nil {
		return reply, err
	}
	if reply.Error != nil {
		return reply, reply.Error
	}
	return reply, nil
}
