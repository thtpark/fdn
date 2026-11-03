package mobile

import (
	"context"
	"errors"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
)

func (r *Runtime) openSharingHuman(ctx context.Context, q Request, f *foreground, input humanOpen) (any, error) {
	if input.GrantID != "" || input.Provider != "" || input.HelpMessageID != "" {
		return nil, domain.Fail(domain.Invalid, "Founder sharing uses current role and selected resource, not another grant/provider/help scope")
	}
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	r.mu.RLock()
	previous := r.human
	r.mu.RUnlock()
	if previous != nil {
		select {
		case <-previous.done:
		default:
			return nil, domain.Fail(domain.Conflict, "close the current human viewer before switching target")
		}
	}
	key := "sharing-open:" + q.OperationID
	var proposal humanProposal
	found, err := f.state.Get(ctx, key, &proposal)
	if err != nil {
		return nil, err
	}
	if !found {
		proposal.Request, err = api.PrepareSharing(ctx, f.client, input.TargetID, input.Mode, "", domain.MaxSharingSeconds, "Explicit Founder mobile selected-resource view")
		if err != nil {
			return nil, err
		}
		proposal.Request.Command.OperationID = "sharing_" + domain.Hash(q.OperationID)
		if err := f.state.Put(ctx, key, proposal); err != nil {
			return nil, err
		}
	} else if proposal.Request.TargetID != input.TargetID || proposal.Request.Mode != input.Mode || proposal.Request.Purpose != "sharing" {
		return nil, domain.Fail(domain.Conflict, "viewer operation belongs to another exact target/mode")
	}
	session, err := api.Call[domain.Session](ctx, f.client, "POST", "/v1/sessions", proposal.Request)
	if err != nil {
		return nil, err
	}
	proposal.SessionID = session.ID
	if err := f.state.Put(ctx, key, proposal); err != nil {
		return nil, err
	}
	session, err = api.Call[domain.Session](ctx, f.client, "POST", api.Path("sessions", session.ID), domain.SessionChange{
		Command: domain.Command{OperationID: q.OperationID + ".open", ExpectedRevision: session.Revision}, Action: "open"})
	if err != nil {
		return nil, err
	}
	view, err := r.attachHuman(ctx, q, f, input, session)
	if err != nil {
		return nil, errors.Join(err, domain.Fail(domain.Prerequisite, "sharing creation is retained; inspect its exact operation before reopening"))
	}
	return view, nil
}
