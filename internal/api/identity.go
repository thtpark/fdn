package api

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/device"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/ingress"
	"github.com/thtpark/fdn/internal/pki"
	"github.com/thtpark/fdn/internal/secure"
)

type IdentityOptions struct {
	CAFile    string
	CAKeyFile string
	Provision func(context.Context, domain.AgentIdentity) (domain.AgentIdentity, error)
	Disable   func(context.Context, domain.AgentIdentity) error
}

type controllerOwner struct {
	key           string
	keyGeneration int64
	instance      string
	generation    int64
	conn          ownerConnection
	ctx           context.Context
	cancel        context.CancelFunc
}

type ownerConnection interface {
	Ping(context.Context) error
	CloseNow() error
	Close(websocket.StatusCode, string) error
}

type identityServer struct {
	server  *Server
	options IdentityOptions
	ca, key []byte
	mu      sync.RWMutex
	owners  map[string]*controllerOwner
	locks   map[string]*sync.Mutex
	closed  bool
}

func newIdentityServer(s *Server) *identityServer {
	return &identityServer{server: s, owners: map[string]*controllerOwner{}, locks: map[string]*sync.Mutex{}}
}

func (s *Server) ConfigureIdentity(options IdentityOptions) error {
	ca, err := secure.ReadPublic(options.CAFile, domain.MaxBody)
	if err != nil {
		return err
	}
	key, err := secure.Read(options.CAKeyFile, domain.MaxBody)
	if err != nil {
		return err
	}
	s.identity.options, s.identity.ca, s.identity.key = options, ca, key
	return nil
}

func (s *identityServer) lock(id string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locks[id] == nil {
		s.locks[id] = new(sync.Mutex)
	}
	return s.locks[id]
}

func (s *identityServer) owner(id string, identity domain.CertificateIdentity) context.Context {
	s.mu.RLock()
	defer s.mu.RUnlock()
	owner := s.owners[id]
	if owner == nil || owner.generation != identity.OwnerGeneration || owner.keyGeneration != identity.Generation || owner.instance != identity.InstanceID {
		return nil
	}
	return owner.ctx
}

func (s *identityServer) retire(id string) {
	s.mu.Lock()
	owner := s.owners[id]
	delete(s.owners, id)
	s.mu.Unlock()
	if owner != nil {
		owner.cancel()
		owner.conn.CloseNow()
	}
}

func (s *identityServer) denyOlder(id string, generation int64, reason string) {
	s.mu.Lock()
	owner := s.owners[id]
	if owner != nil && owner.keyGeneration < generation {
		delete(s.owners, id)
	} else {
		owner = nil
	}
	s.mu.Unlock()
	if owner != nil {
		owner.conn.Close(websocket.StatusPolicyViolation, reason)
		owner.cancel()
	}
}

func (s *Server) disableIdentity(ctx context.Context, id string) {
	if s.identity.options.Disable == nil {
		return
	}
	cleanupCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer stop()
	identity, err := s.Store.Identity(cleanupCtx, id)
	if err == nil {
		if identity.State != "banned" && identity.State != "revoked" && identity.State != "restore-pending" {
			return
		}
		err = s.identity.options.Disable(cleanupCtx, identity)
	}
	if err != nil {
		s.Log.Warn("identity account cleanup pending", "agent_id", id)
	}
}

func (s *identityServer) close() {
	s.mu.Lock()
	s.closed = true
	owners := s.owners
	s.owners = map[string]*controllerOwner{}
	s.mu.Unlock()
	for _, o := range owners {
		o.cancel()
		o.conn.CloseNow()
	}
}

func (s *Server) authenticateConnection(r *http.Request, purpose string) (domain.Agent, context.Context, error) {
	if s.authentication != nil {
		actor, err := s.authentication(r)
		if err == nil {
			recordActor(r.Context(), actor.ID, domain.CertificateIdentity{})
		}
		return actor, nil, err
	}
	denied := func() (domain.Agent, context.Context, error) {
		return domain.Agent{}, nil, domain.Fail(domain.Denied, "a verified account-bound client identity is required")
	}
	uid, ok := ingress.UID(r.Context())
	if !ok || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		return denied()
	}
	cert := r.TLS.PeerCertificates[0]
	if time.Now().Before(cert.NotBefore) || !time.Now().Before(cert.NotAfter) {
		return denied()
	}
	identity, err := pki.ParseIdentity(cert)
	if err != nil || identity.Purpose != purpose {
		return denied()
	}
	actor, err := s.Store.ValidateCertificate(r.Context(), int64(uid), identity, cert.PublicKey)
	if err != nil {
		return domain.Agent{}, nil, err
	}
	recordActor(r.Context(), actor.ID, identity)
	if purpose != "owner" {
		return actor, nil, nil
	}
	owner := s.identity.owner(actor.ID, identity)
	if owner == nil {
		return denied()
	}
	select {
	case <-owner.Done():
		return denied()
	default:
	}
	return actor, owner, nil
}

func (s *Server) provisionIdentity(ctx context.Context, proposed domain.AgentIdentity) (domain.AgentIdentity, error) {
	current, err := s.Store.Identity(ctx, proposed.AgentID)
	if err != nil {
		return current, err
	}
	if current.Generation != proposed.Generation || current.RequestID != proposed.RequestID || current.SSHFingerprint != proposed.SSHFingerprint || current.AuthFingerprint != proposed.AuthFingerprint {
		return current, domain.Fail(domain.Conflict, "identity registration belongs to a retired key generation")
	}
	switch current.State {
	case "active":
		return current, nil
	case "pending", "provisioned":
	default:
		return current, domain.Fail(domain.Denied, "identity requires explicit key recovery before provisioning")
	}
	s.identity.denyOlder(current.AgentID, current.Generation, "identity keys replaced; use the approved recovery profile")
	if s.identity.options.Provision == nil {
		return current, nil
	}
	return s.identity.options.Provision(ctx, current)
}

func (s *Server) identityRoutes() {
	body(s, "POST /v1/identity/register", false, func(r *operationRequest, a domain.Agent, q domain.RegisterIdentityRequest) (domain.AgentIdentity, error) {
		identity, err := s.Store.RegisterIdentity(r.Context(), a.ID, q)
		if err != nil {
			return identity, err
		}
		return s.provisionIdentity(r.Context(), identity)
	})
	read(s, "GET /v1/identity/status", func(r *operationRequest, a domain.Agent) (domain.AgentIdentity, error) {
		return s.Store.Identity(r.Context(), a.ID)
	})
	body(s, "POST /v1/identity/{id}/recover", false, func(r *operationRequest, a domain.Agent, q domain.RecoverIdentityRequest) (domain.AgentIdentity, error) {
		if a.Founder && r.PathValue("id") == a.ID {
			return domain.AgentIdentity{}, domain.Fail(domain.Prerequisite, "Founder key replacement requires the local OS-operator recovery channel")
		}
		identity, err := s.Store.RecoverIdentity(r.Context(), a.ID, r.PathValue("id"), q)
		if err != nil {
			return identity, err
		}
		return s.provisionIdentity(r.Context(), identity)
	})
}

// BootstrapHandler exposes only key-bound admission and the device control channel.
func (s *Server) BootstrapHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/identity/challenge", func(w http.ResponseWriter, r *http.Request) {
		uid, ok := ingress.UID(r.Context())
		if !ok {
			s.respond(w, r, nil, "", domain.Fail(domain.Denied, "registered SSH account required"))
			return
		}
		challenge, err := s.Store.ChallengeIdentity(r.Context(), int64(uid))
		if err == nil {
			recordActor(r.Context(), challenge.AgentID, domain.CertificateIdentity{Generation: challenge.Generation})
		}
		s.respond(w, r, challenge, "", err)
	})
	mux.HandleFunc("POST /v1/identity/activate", func(w http.ResponseWriter, r *http.Request) {
		uid, ok := ingress.UID(r.Context())
		if !ok {
			s.respond(w, r, nil, "", domain.Fail(domain.Denied, "registered SSH account required"))
			return
		}
		var req domain.ActivateIdentityRequest
		op, err := readBody(w, r, &req)
		if err != nil {
			s.respond(w, r, nil, op, err)
			return
		}
		identity, err := s.Store.ActivateIdentity(r.Context(), int64(uid), req)
		if err != nil {
			s.respond(w, r, nil, op, err)
			return
		}
		recordActor(r.Context(), identity.AgentID, domain.CertificateIdentity{Generation: identity.Generation})
		public, err := device.PublicKey(identity.AuthKey)
		if err != nil {
			s.respond(w, r, nil, op, err)
			return
		}
		certificate, err := s.identity.issue(identity, public, "device")
		if err != nil {
			s.respond(w, r, nil, op, err)
			return
		}
		agent, err := s.Store.Me(r.Context(), identity.AgentID)
		s.respond(w, r, domain.IdentityCertificate{Agent: agent, Identity: identity, Certificate: string(certificate)}, op, err)
	})
	mux.HandleFunc("GET /v1/identity/owner", s.serveOwner)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.respond(w, r, nil, "", domain.Fail(domain.Denied, "bootstrap channel cannot access ordinary API routes"))
	})
	return s.access(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Fdn-API-Version", strconv.Itoa(domain.APIVersion))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.TLS == nil || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "https://"+r.Host) {
			s.respond(w, r, nil, "", domain.Fail(domain.Denied, "verified TLS origin required"))
			return
		}
		mux.ServeHTTP(w, r)
		recordRoute(r)
	}))
}

func (s *identityServer) issue(identity domain.AgentIdentity, public crypto.PublicKey, purpose string) ([]byte, error) {
	if len(s.ca) == 0 || len(s.key) == 0 {
		return nil, domain.Fail(domain.Prerequisite, "identity certificate issuer is not configured")
	}
	info := domain.CertificateIdentity{InstanceID: identity.InstanceID, AgentID: identity.AgentID, Generation: identity.Generation, Purpose: purpose}
	ttl := 24 * time.Hour
	if purpose == "owner" {
		info.OwnerGeneration = identity.OwnerGeneration
		ttl = 15 * time.Minute
	}
	return pki.IssueClient(s.ca, s.key, public, info, time.Now(), ttl)
}

func readOwner(ctx context.Context, conn *websocket.Conn) (domain.OwnerRequest, error) {
	var req domain.OwnerRequest
	_, data, err := conn.Read(ctx)
	if err != nil {
		return req, err
	}
	err = config.Decode(data, &req)
	return req, err
}

func canonicalOwner(public crypto.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		return "", domain.Fail(domain.Invalid, "invalid controller owner key")
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}

func ownerChallenge(identity domain.AgentIdentity, ttl time.Duration) domain.IdentityChallenge {
	return domain.IdentityChallenge{Nonce: domain.NewToken(), AgentID: identity.AgentID, InstanceID: identity.InstanceID, Generation: identity.Generation, ExpiresAt: time.Now().Add(ttl).Unix()}
}

func (s *Server) admitOwner(ctx, lifetime context.Context, actor domain.Agent, identity domain.AgentIdentity, request domain.OwnerRequest, conn ownerConnection) (*controllerOwner, domain.AgentIdentity, error) {
	lock := s.identity.lock(actor.ID)
	lock.Lock()
	defer lock.Unlock()
	fresh, err := s.Store.Identity(ctx, actor.ID)
	if err != nil {
		return nil, identity, err
	}
	if fresh.State != "active" || fresh.InstanceID != identity.InstanceID || fresh.Generation != identity.Generation {
		return nil, identity, domain.Fail(domain.Denied, "identity changed before owner admission")
	}
	identity = fresh
	s.identity.mu.RLock()
	old := s.identity.owners[actor.ID]
	closed := s.identity.closed
	s.identity.mu.RUnlock()
	if closed {
		return nil, identity, domain.Fail(domain.Unavailable, "Foundation is stopping")
	}
	if old != nil && (old.keyGeneration != identity.Generation || old.instance != identity.InstanceID || old.generation != identity.OwnerGeneration || old.key != identity.OwnerKey) {
		s.identity.retire(actor.ID)
		old = nil
	}
	if old != nil && old.key != request.OwnerKey {
		ping, stop := context.WithTimeout(ctx, 2*time.Second)
		live := old.conn.Ping(ping) == nil
		stop()
		if live {
			if err := s.Store.BanDuplicateIdentity(ctx, actor.ID, identity.Generation, old.generation, "confirmed duplicate live controller identities"); err != nil {
				return nil, identity, err
			}
			s.identity.mu.Lock()
			delete(s.identity.owners, actor.ID)
			s.identity.mu.Unlock()
			_ = old.conn.Close(websocket.StatusPolicyViolation, "duplicate identity banned; explicit recovery required")
			old.cancel()
			_ = conn.Close(websocket.StatusPolicyViolation, "duplicate identity banned; explicit recovery required")
			s.Log.Warn("identity banned after confirmed duplicate ownership", "agent_id", actor.ID)
			s.disableIdentity(ctx, actor.ID)
			return nil, identity, domain.Fail(domain.Denied, "duplicate identity banned; explicit recovery required")
		}
	}
	identity, err = s.Store.SetIdentityOwner(ctx, actor.ID, request.OwnerKey, identity.Generation)
	if err != nil {
		return nil, identity, err
	}
	ownerCtx, cancel := context.WithCancel(lifetime)
	entry := &controllerOwner{key: request.OwnerKey, keyGeneration: identity.Generation, instance: identity.InstanceID,
		generation: identity.OwnerGeneration, conn: conn, ctx: ownerCtx, cancel: cancel}
	s.identity.mu.Lock()
	s.identity.owners[actor.ID] = entry
	s.identity.mu.Unlock()
	if old != nil {
		old.cancel()
		_ = old.conn.CloseNow()
	}
	return entry, identity, nil
}

func (s *Server) releaseOwner(actor string, entry *controllerOwner) {
	entry.cancel()
	s.identity.mu.Lock()
	if s.identity.owners[actor] == entry {
		delete(s.identity.owners, actor)
	}
	s.identity.mu.Unlock()
}

func (s *Server) serveOwner(w http.ResponseWriter, r *http.Request) {
	actor, _, err := s.authenticateConnection(r, "device")
	if err != nil {
		s.respond(w, r, nil, "", err)
		return
	}
	if s.authentication == nil {
		ctx, stop := context.WithDeadline(r.Context(), r.TLS.PeerCertificates[0].NotAfter)
		defer stop()
		r = r.WithContext(ctx)
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(16384)
	identity, err := s.Store.Identity(r.Context(), actor.ID)
	if err != nil {
		return
	}
	challenge := ownerChallenge(identity, 10*time.Second)
	initialCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	err = wsjson.Write(initialCtx, conn, challenge)
	if err != nil {
		cancel()
		return
	}
	req, err := readOwner(initialCtx, conn)
	cancel()
	if err != nil || req.Nonce != challenge.Nonce || time.Now().Unix() >= challenge.ExpiresAt {
		return
	}
	public, err := device.VerifyOwner(req, identity)
	if err != nil {
		conn.Close(websocket.StatusPolicyViolation, "invalid owner proof")
		return
	}
	req.OwnerKey, err = canonicalOwner(public)
	if err != nil {
		return
	}
	entry, identity, err := s.admitOwner(r.Context(), r.Context(), actor, identity, req, conn)
	if err != nil {
		conn.Close(websocket.StatusPolicyViolation, "identity owner admission denied")
		return
	}
	ownerCtx := entry.ctx
	defer s.releaseOwner(actor.ID, entry)
	for {
		current, lookupErr := s.Store.Identity(ownerCtx, actor.ID)
		if lookupErr != nil {
			return
		}
		if current.State != "active" || current.Generation != entry.keyGeneration || current.InstanceID != entry.instance || current.OwnerGeneration != entry.generation {
			conn.Close(websocket.StatusPolicyViolation, "identity revoked or keys replaced; explicit recovery required")
			return
		}
		certificate, issueErr := s.identity.issue(current, public, "owner")
		if issueErr != nil {
			return
		}
		challenge = ownerChallenge(current, 15*time.Minute)
		if err := wsjson.Write(ownerCtx, conn, domain.OwnerCertificate{Certificate: string(certificate), Generation: entry.generation, Challenge: challenge}); err != nil {
			return
		}
		next, readErr := readOwner(ownerCtx, conn)
		if readErr != nil {
			return
		}
		if next.Nonce != challenge.Nonce || time.Now().Unix() >= challenge.ExpiresAt {
			return
		}
		nextPublic, err := device.VerifyOwner(next, current)
		if err != nil {
			return
		}
		nextKey, err := canonicalOwner(nextPublic)
		if err != nil || nextKey != entry.key {
			return
		}
	}
}
