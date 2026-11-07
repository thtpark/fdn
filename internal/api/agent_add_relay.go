package api

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message"
)

const AgentAdditionRelayPrefix = "relay.agent-add"
const AgentAdditionRelayOperation = AgentAdditionRelayPrefix + ".open"

func additionRelayAddress(input domain.AgentAdditionRelayOpen) (string, error) {
	address, err := netip.ParseAddr(input.Host)
	if err != nil || address.IsUnspecified() || address.IsMulticast() || address.Zone() != "" ||
		input.Port < 1 || input.Port > 65535 ||
		!domain.ValidOperation(input.OperationID) || !domain.ValidOperation(input.Ticket) {
		return "", domain.Fail(domain.Invalid, "agent addition relay requires an exact operation, ticket, literal IP and SSH port 1-65535")
	}
	return net.JoinHostPort(input.Host, strconv.Itoa(input.Port)), nil
}

func (p *relayPeer) additionFounder(ctx context.Context) error {
	actor, err := p.server.Store.Me(ctx, p.actor.ID)
	if err != nil {
		return err
	}
	if !actor.Founder {
		return domain.Fail(domain.Denied, "only active Founder may relay SSH for a new agent")
	}
	return nil
}

func (p *relayPeer) openAgentAddition(ctx context.Context, request message.Request) ([]byte, error) {
	if err := p.additionFounder(ctx); err != nil {
		return nil, err
	}
	if request.Target != "" {
		return nil, domain.Fail(domain.Invalid, "agent addition relay does not accept a target override")
	}
	var input domain.AgentAdditionRelayOpen
	if err := message.UnmarshalPayload(request.Payload, &input); err != nil {
		return nil, err
	}
	address, err := additionRelayAddress(input)
	if err != nil {
		return nil, err
	}
	return p.open(ctx, input.Ticket, &relayStream{kind: additionRelay}, func() (net.Conn, error) {
		dial, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		conn, err := (&net.Dialer{}).DialContext(dial, "tcp", address)
		code := "ok"
		if err != nil {
			code = string(domain.Unavailable)
		}
		p.server.Log.Info("Foundation agent addition relay", "actor_id", p.actor.ID,
			"operation_id", input.OperationID, "code", code)
		if err != nil {
			return nil, domain.Fail(domain.Unavailable, "Foundation could not reach the new worker SSH endpoint")
		}
		return conn, nil
	})
}

func DialAgentAdditionRelay(ctx context.Context, client *Client, operationID, host string, port int) (net.Conn, error) {
	if !client.IsMessageClient() {
		return nil, domain.Fail(domain.Prerequisite, "agent addition relay requires the authenticated native Foundation transport")
	}
	input := domain.AgentAdditionRelayOpen{
		OperationID: operationID, Host: host, Port: port, Ticket: domain.NewID("relay"),
	}
	if _, err := additionRelayAddress(input); err != nil {
		return nil, err
	}
	return dialMessageRelay(ctx, client, AgentAdditionRelayOperation, input.Ticket, input)
}
