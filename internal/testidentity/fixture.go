// Package testidentity seeds current signed identities for isolated business fixtures.
package testidentity

import (
	"context"
	"net/http"

	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/foundation"
)

const ActorHeader = "X-Fdn-Test-Actor"

func Register(ctx context.Context, store *foundation.Store, founder, name string, uid int64) (domain.Agent, error) {
	public, _, private, err := device.Generate(name)
	if err != nil {
		return domain.Agent{}, err
	}
	var identity domain.AgentIdentity
	if founder == "" {
		identity, err = store.BootstrapIdentity(ctx, public, domain.NewID("op"))
	} else {
		identity, err = store.RegisterIdentity(ctx, founder, domain.RegisterIdentityRequest{
			Command: domain.Command{OperationID: domain.NewID("op")}, Identity: public})
	}
	if err != nil {
		return domain.Agent{}, err
	}
	identity, err = store.BindIdentityAccount(ctx, identity.AgentID, identity.SSHUser, uid, identity.SSHFingerprint)
	if err != nil {
		return domain.Agent{}, err
	}
	challenge, err := store.ChallengeIdentity(ctx, uid)
	if err != nil {
		return domain.Agent{}, err
	}
	signer, err := device.ParseSigner(private)
	if err != nil {
		return domain.Agent{}, err
	}
	proof, err := device.SignChallenge(signer, challenge)
	if err != nil {
		return domain.Agent{}, err
	}
	identity, err = store.ActivateIdentity(ctx, uid, domain.ActivateIdentityRequest{
		Command: domain.Command{OperationID: domain.NewID("op")}, Nonce: challenge.Nonce, Proof: proof})
	if err != nil {
		return domain.Agent{}, err
	}
	return store.Me(ctx, identity.AgentID)
}

func Authenticate(store *foundation.Store, request *http.Request) (domain.Agent, error) {
	return store.Me(request.Context(), request.Header.Get(ActorHeader))
}

type actorTransport struct {
	base  http.RoundTripper
	actor string
}

func Transport(base http.RoundTripper, actor string) http.RoundTripper {
	return actorTransport{base: base, actor: actor}
}

func (t actorTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set(ActorHeader, t.actor)
	return t.base.RoundTrip(request)
}
