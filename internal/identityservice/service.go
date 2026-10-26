package identityservice

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"time"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/transport"
)

var errRenewDevice = errors.New("device certificate renewal is due")

type Service struct {
	config      config.IdentityService
	options     Options
	mu          sync.RWMutex
	status      api.LocalStatus
	profile     config.ConnectionProfile
	lease       *ownerLease
	ownerExpiry time.Time
}

type ownerLease struct {
	client  *api.Client
	context context.Context
	expires time.Time
}

func New(c config.IdentityService, options Options) (*Service, error) {
	if err := validateSettings(c); err != nil {
		return nil, err
	}
	return &Service{config: c, options: options.defaults()}, nil
}

func (s *Service) Status() api.LocalStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	status := s.status
	if s.lease == nil || !s.lease.expires.After(s.options.Now()) || s.lease.context.Err() != nil {
		status.Ready = false
	}
	if !s.ownerExpiry.IsZero() && !s.ownerExpiry.After(s.options.Now()) {
		status.Ready, status.Connected, status.Problem = false, false, domain.Denied
	}
	return status
}

func (s *Service) Serve(ctx context.Context) error {
	return s.ServeWithBoundary(ctx, NativeBoundary{})
}

// ServeWithBoundary is the embedding/test seam. Native serving has no flag or
// configuration switch replacing account isolation or kernel peer checks.
func (s *Service) ServeWithBoundary(ctx context.Context, boundary Boundary) error {
	if boundary == nil {
		return domain.Fail(domain.Invalid, "identity IPC boundary is required")
	}
	if _, native := boundary.(NativeBoundary); native {
		if err := CheckNativeAccount(s.config); err != nil {
			return err
		}
	}
	lock, err := stateLock(s.config.StateDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	d, signer, err := loadDevice(s.config.StateDir, s.options.Cryptography)
	if err != nil {
		return err
	}
	var profile config.ConnectionProfile
	if err := config.Read(filepath.Join(s.config.StateDir, profileFile), &profile); err != nil {
		return err
	}
	worker, err := bindProfile(s.config, profile, d)
	if err != nil {
		return err
	}
	owner, err := s.options.Cryptography.GenerateOwner()
	if err != nil {
		return err
	}
	if _, ok := owner.Public().(ed25519.PublicKey); !ok {
		return domain.Fail(domain.Denied, "owner proof requires a new ephemeral Ed25519 key")
	}
	ownerPEM, err := ownerKeyPEM(owner)
	if err != nil {
		return err
	}
	listener, err := boundary.Listen(s.config)
	if err != nil {
		return err
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.mu.Lock()
	s.status = api.LocalStatus{Identity: profile.Identity}
	s.profile = profile
	s.ownerExpiry = time.Time{}
	s.mu.Unlock()
	served := make(chan error, 1)
	go func() {
		served <- transport.ServeConnections(ctx,
			authorizedListener{Listener: listener, boundary: boundary, uid: s.config.WorkerUID, gid: s.config.WorkerGID},
			s.serveMessagePeer, func(err error) {
				s.options.Log.Warn("identity IPC message connection closed", "code", domain.AsFault(err).Code)
			})
	}()
	ownerDone := make(chan struct{})
	go func() {
		defer close(ownerDone)
		s.maintainOwner(ctx, profile, d, signer, owner, ownerPEM, worker)
	}()
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-served:
		served = nil
	}
	cancel()
	closeErr := listener.Close()
	if errors.Is(closeErr, net.ErrClosed) {
		closeErr = nil
	}
	if served != nil {
		serveErr = <-served
	}
	<-ownerDone
	if errors.Is(serveErr, net.ErrClosed) {
		serveErr = nil
	}
	return errors.Join(serveErr, closeErr)
}

func (s *Service) maintainOwner(ctx context.Context, profile config.ConnectionProfile, d privateDevice, signer, owner crypto.Signer, ownerPEM []byte, worker config.Worker) {
	delay := s.options.RetryInterval
	for ctx.Err() == nil {
		issued, cert, err := activate(ctx, s.config, profile, d, signer, worker, s.options)
		if err == nil {
			s.mu.Lock()
			s.status.Agent, s.status.Identity, s.status.Connected = issued.Agent, issued.Identity, true
			s.mu.Unlock()
			err = s.runOwner(ctx, profile, worker, cert, owner, ownerPEM)
		}
		s.mu.Lock()
		s.status.Ready = false
		s.status.Problem = domain.AsFault(err).Code
		if errors.Is(err, errRenewDevice) {
			s.status.Problem = domain.Unavailable
		}
		if s.status.Problem == domain.Denied || s.status.Problem == domain.Conflict {
			s.status.Problem, s.status.Connected = domain.Denied, false
		}
		s.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errRenewDevice) {
			delay = s.options.RetryInterval
			continue
		}
		code := domain.AsFault(err).Code
		if code == domain.Denied || code == domain.Conflict || code == domain.Invalid || code == domain.Prerequisite {
			<-ctx.Done()
			return
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = min(delay*2, 30*time.Second)
	}
}

func validChallenge(challenge domain.IdentityChallenge, identity domain.AgentIdentity, now time.Time) bool {
	return challenge.AgentID == identity.AgentID && challenge.InstanceID == identity.InstanceID &&
		challenge.Generation == identity.Generation && challenge.Nonce != "" && challenge.ExpiresAt > now.Unix()
}

func (s *Service) runOwner(ctx context.Context, profile config.ConnectionProfile, worker config.Worker, deviceCert tls.Certificate, signer crypto.Signer, keyPEM []byte) (result error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := s.options.Remote.Owner(ctx, worker, deviceCert)
	if err != nil {
		return err
	}
	defer stream.Close()
	challengeCtx, challengeCancel := context.WithTimeout(ctx, 10*time.Second)
	challenge, err := stream.Challenge(challengeCtx)
	challengeCancel()
	if err != nil {
		return err
	}
	if !validChallenge(challenge, profile.Identity, s.options.Now()) {
		return domain.Fail(domain.Denied, "owner challenge does not match the pinned identity or has expired")
	}
	type received struct {
		certificate domain.OwnerCertificate
		err         error
	}
	updates := make(chan received, 1)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			certificate, err := stream.Receive(ctx)
			select {
			case updates <- received{certificate, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	leaseCtx, leaseCancel := context.WithCancel(ctx)
	clients := []*api.Client{}
	defer func() {
		s.mu.Lock()
		s.lease = nil
		s.status.Ready = false
		if code := domain.AsFault(result).Code; code == domain.Denied || code == domain.Conflict {
			s.status.Problem, s.status.Connected = domain.Denied, false
		}
		s.mu.Unlock()
		cancel()
		leaseCancel()
		stream.Close()
		<-readDone
		for _, client := range clients {
			client.Close()
		}
	}()
	awaiting := false
	send := func(challenge domain.IdentityChallenge) error {
		if !validChallenge(challenge, profile.Identity, s.options.Now()) {
			return domain.Fail(domain.Denied, "owner renewal challenge is invalid or expired")
		}
		request, err := s.options.Cryptography.SignOwner(signer, profile.Identity, challenge.Nonce)
		if err != nil {
			return err
		}
		sendCtx, sendCancel := context.WithTimeout(ctx, 10*time.Second)
		defer sendCancel()
		if err := stream.Send(sendCtx, request); err != nil {
			return err
		}
		awaiting = true
		return nil
	}
	if err := send(challenge); err != nil {
		return err
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	renew := time.NewTimer(time.Hour)
	renew.Stop()
	defer renew.Stop()
	expiry := time.NewTimer(time.Hour)
	expiry.Stop()
	defer expiry.Stop()
	deviceRenew := time.NewTimer(max(deviceCert.Leaf.NotAfter.Sub(s.options.Now())-s.options.DeviceRenewBefore, time.Millisecond))
	defer deviceRenew.Stop()
	var generation int64
	for {
		select {
		case <-deviceRenew.C:
			return errRenewDevice
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return domain.Fail(domain.Unavailable, "owner certificate response timed out")
		case <-expiry.C:
			return domain.Fail(domain.Unavailable, "owner certificate expired before renewal; reacquiring with the same ephemeral key")
		case <-renew.C:
			if err := send(challenge); err != nil {
				return err
			}
			deadline.Reset(10 * time.Second)
		case update := <-updates:
			if update.err != nil {
				return update.err
			}
			certificate := update.certificate
			if !awaiting || certificate.Generation < 1 || certificate.Generation < generation ||
				!validChallenge(certificate.Challenge, profile.Identity, s.options.Now()) || certificate.Challenge.Nonce == challenge.Nonce {
				return domain.Fail(domain.Denied, "owner certificate response has invalid generation or next challenge")
			}
			pair, err := validateCertificate(certificate.Certificate, keyPEM, profile, "owner", certificate.Generation, s.options.Now())
			if err != nil {
				return err
			}
			client, err := s.options.Remote.Client(worker, pair)
			if err != nil {
				return err
			}
			if client == nil || (!client.IsMessageClient() && (client.HTTP == nil || client.HTTP.Transport == nil)) || client.Credential.Token != "" || client.BaseURL != worker.URL {
				if client != nil {
					client.Close()
				}
				return domain.Fail(domain.Denied, "owner transport must use pinned trust without bearer credentials")
			}
			if _, native := s.options.Remote.(NativeRemote); native && !client.IsMessageClient() {
				client.Close()
				return domain.Fail(domain.Denied, "native owner transport requires the binary SSH protocol")
			}
			if len(clients) > 0 {
				clients[0].Close()
			}
			clients = []*api.Client{client}
			generation, challenge, awaiting = certificate.Generation, certificate.Challenge, false
			deadline.Stop()
			s.mu.Lock()
			s.lease = &ownerLease{client: client, context: leaseCtx, expires: pair.Leaf.NotAfter}
			s.ownerExpiry = pair.Leaf.NotAfter
			s.status.Ready, s.status.OwnerGeneration, s.status.Problem = true, generation, ""
			s.mu.Unlock()
			remaining := pair.Leaf.NotAfter.Sub(s.options.Now())
			expiry.Reset(remaining)
			untilChallengeExpiry := time.Unix(challenge.ExpiresAt, 0).Sub(s.options.Now())
			renew.Reset(min(s.options.RenewInterval, remaining/3, untilChallengeExpiry/2))
		}
	}
}
