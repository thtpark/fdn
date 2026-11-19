package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"strconv"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/secure"
	"github.com/thtpark/fdn/internal/software"
	"github.com/thtpark/fdn/internal/upgrade"
)

func (s *Server) ConfigureDistributionKey(path string) error {
	key, err := secure.Read(path, 16<<10)
	if err != nil {
		return err
	}
	block, _ := pem.Decode(key)
	if block == nil {
		return domain.Fail(domain.Prerequisite, "distribution signer must be a protected Ed25519 PKCS8 key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return err
	}
	private, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return domain.Fail(domain.Prerequisite, "distribution signer must use Ed25519")
	}
	if caBlock, _ := pem.Decode(s.identity.key); caBlock != nil {
		ca, err := x509.ParsePKCS8PrivateKey(caBlock.Bytes)
		if err != nil {
			return err
		}
		if signing, ok := ca.(ed25519.PrivateKey); ok && bytes.Equal(private.Public().(ed25519.PublicKey), signing.Public().(ed25519.PublicKey)) {
			return domain.Fail(domain.Denied, "artifact distribution signing must not reuse the enrollment/permission CA")
		}
	}
	s.distributionKey = bytes.Clone(key)
	return nil
}

func deliveryBody[Q any, R any](s *Server, pattern string, call func(*operationRequest, domain.Agent, Q) (R, error)) {
	body(s, pattern, false, func(r *operationRequest, a domain.Agent, q Q) (R, error) {
		var zero R
		if r.URL.RawQuery != "" {
			return zero, domain.Fail(domain.Invalid, "delivery mutations do not accept query fields")
		}
		return call(r, a, q)
	})
}

func (s *Server) deliveryRoutes() {
	read(s, "GET /v1/improvement-policy", func(r *operationRequest, a domain.Agent) (domain.ImprovementPolicy, error) {
		if err := publicationRead(r); err != nil {
			return domain.ImprovementPolicy{}, err
		}
		return s.Store.ImprovementPolicy(r.Context(), a.ID)
	})
	deliveryBody(s, "POST /v1/improvement-policy", func(r *operationRequest, a domain.Agent, q domain.ImprovementPolicyRequest) (domain.ImprovementPolicy, error) {
		return s.Store.ConfigureImprovements(r.Context(), a.ID, q)
	})
	read(s, "GET /v1/improvements", func(r *operationRequest, a domain.Agent) (domain.Page[domain.Improvement], error) {
		if err := publicationRead(r, "project", "state", "cursor", "limit"); err != nil {
			return domain.Page[domain.Improvement]{}, err
		}
		limit, err := queryLimit(r)
		if err != nil {
			return domain.Page[domain.Improvement]{}, err
		}
		q := r.URL.Query()
		return s.Store.Improvements(r.Context(), a.ID, q.Get("project"), q.Get("state"), q.Get("cursor"), limit)
	})
	read(s, "GET /v1/improvements/{id}", func(r *operationRequest, a domain.Agent) (domain.Improvement, error) {
		if err := publicationRead(r); err != nil {
			return domain.Improvement{}, err
		}
		return s.Store.Improvement(r.Context(), a.ID, r.PathValue("id"))
	})
	deliveryBody(s, "POST /v1/improvements", func(r *operationRequest, a domain.Agent, q domain.ImprovementRequest) (domain.Improvement, error) {
		return s.Store.CreateImprovement(r.Context(), a.ID, q)
	})
	deliveryBody(s, "POST /v1/improvements/{id}/decision", func(r *operationRequest, a domain.Agent, q domain.ImprovementDecision) (domain.ImprovementResult, error) {
		return s.Store.DecideImprovement(r.Context(), a.ID, r.PathValue("id"), q)
	})
	read(s, "GET /v1/maintenance-usage", func(r *operationRequest, a domain.Agent) (domain.MaintenanceUsage, error) {
		if err := publicationRead(r); err != nil {
			return domain.MaintenanceUsage{}, err
		}
		return s.Store.MaintenanceUsage(r.Context(), a.ID)
	})
	read(s, "GET /v1/maintenance-reservations/{id}", func(r *operationRequest, a domain.Agent) (domain.MaintenanceStatus, error) {
		if err := publicationRead(r); err != nil {
			return domain.MaintenanceStatus{}, err
		}
		return s.Store.MaintenanceReservation(r.Context(), a.ID, r.PathValue("id"))
	})
	deliveryBody(s, "POST /v1/maintenance-reservations", func(r *operationRequest, a domain.Agent, q domain.MaintenanceRequest) (domain.MaintenanceReservation, error) {
		return s.Store.ReserveMaintenance(r.Context(), a.ID, q)
	})
	deliveryBody(s, "POST /v1/maintenance-reservations/{id}/settle", func(r *operationRequest, a domain.Agent, q domain.MaintenanceSettlement) (domain.MaintenanceReservation, error) {
		return s.Store.SettleMaintenance(r.Context(), a.ID, r.PathValue("id"), q)
	})
	deliveryBody(s, "POST /v1/repositories", func(r *operationRequest, a domain.Agent, q domain.RepositoryRequest) (domain.HostedRepository, error) {
		return s.Store.CreateRepository(r.Context(), a.ID, q)
	})
	read(s, "GET /v1/repositories", func(r *operationRequest, a domain.Agent) (domain.Page[domain.HostedRepository], error) {
		if err := publicationRead(r, "project", "cursor", "limit"); err != nil {
			return domain.Page[domain.HostedRepository]{}, err
		}
		limit, err := queryLimit(r)
		if err != nil {
			return domain.Page[domain.HostedRepository]{}, err
		}
		return s.Store.Repositories(r.Context(), a.ID, r.URL.Query().Get("project"), r.URL.Query().Get("cursor"), limit)
	})
	read(s, "GET /v1/repositories/{id}", func(r *operationRequest, a domain.Agent) (domain.HostedRepository, error) {
		if err := publicationRead(r); err != nil {
			return domain.HostedRepository{}, err
		}
		return s.Store.Repository(r.Context(), a.ID, r.PathValue("id"))
	})
	read(s, "GET /v1/repositories/{id}/file", func(r *operationRequest, a domain.Agent) (domain.RepositoryFile, error) {
		if err := publicationRead(r, "commit", "path"); err != nil {
			return domain.RepositoryFile{}, err
		}
		return s.Store.RepositoryFile(r.Context(), a.ID, r.PathValue("id"), r.URL.Query().Get("commit"), r.URL.Query().Get("path"))
	})
	deliveryBody(s, "POST /v1/repositories/{id}/push", func(r *operationRequest, a domain.Agent, q domain.RepositoryPush) (domain.HostedRepository, error) {
		return s.Store.PushRepository(r.Context(), a.ID, r.PathValue("id"), q)
	})
	deliveryBody(s, "POST /v1/repositories/{id}/merge", func(r *operationRequest, a domain.Agent, q domain.RepositoryMerge) (domain.HostedRepository, error) {
		return s.Store.MergeRepository(r.Context(), a.ID, r.PathValue("id"), q)
	})
	deliveryBody(s, "POST /v1/repositories/{id}/upstream", func(r *operationRequest, a domain.Agent, q domain.RepositoryMerge) (domain.HostedRepository, error) {
		return s.Store.UpstreamRepository(r.Context(), a.ID, r.PathValue("id"), q)
	})
	read(s, "GET /v1/artifacts/{id}", func(r *operationRequest, a domain.Agent) (domain.DeliveryArtifact, error) {
		if err := publicationRead(r); err != nil {
			return domain.DeliveryArtifact{}, err
		}
		return s.Store.Artifact(r.Context(), a.ID, r.PathValue("id"))
	})
	read(s, "GET /v1/artifacts/{id}/chunks", func(r *operationRequest, a domain.Agent) (domain.FileChunk, error) {
		if err := publicationRead(r, "offset"); err != nil {
			return domain.FileChunk{}, err
		}
		offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
		if err != nil {
			return domain.FileChunk{}, domain.Fail(domain.Invalid, "artifact offset must be an integer")
		}
		return s.Store.ArtifactChunk(r.Context(), a.ID, r.PathValue("id"), offset)
	})
	deliveryBody(s, "POST /v1/software-products", func(r *operationRequest, a domain.Agent, q domain.SoftwareProductRequest) (domain.SoftwareProduct, error) {
		return s.Store.CreateSoftwareProduct(r.Context(), a.ID, q)
	})
	read(s, "GET /v1/software-products", func(r *operationRequest, a domain.Agent) (domain.Page[domain.SoftwareProduct], error) {
		if err := publicationRead(r, "project", "cursor", "limit"); err != nil {
			return domain.Page[domain.SoftwareProduct]{}, err
		}
		limit, err := queryLimit(r)
		if err != nil {
			return domain.Page[domain.SoftwareProduct]{}, err
		}
		return s.Store.SoftwareProducts(r.Context(), a.ID, r.URL.Query().Get("project"), r.URL.Query().Get("cursor"), limit)
	})
	read(s, "GET /v1/software-products/{id}", func(r *operationRequest, a domain.Agent) (domain.SoftwareProduct, error) {
		if err := publicationRead(r); err != nil {
			return domain.SoftwareProduct{}, err
		}
		return s.Store.SoftwareProduct(r.Context(), a.ID, r.PathValue("id"))
	})
	deliveryBody(s, "POST /v1/software-builds", func(r *operationRequest, a domain.Agent, q domain.SoftwareBuildRequest) (domain.SoftwareBuild, error) {
		return s.Store.CreateSoftwareBuild(r.Context(), a.ID, q)
	})
	read(s, "GET /v1/software-builds/{id}", func(r *operationRequest, a domain.Agent) (domain.SoftwareBuild, error) {
		if err := publicationRead(r); err != nil {
			return domain.SoftwareBuild{}, err
		}
		return s.Store.SoftwareBuild(r.Context(), a.ID, r.PathValue("id"))
	})
	deliveryBody(s, "POST /v1/software-builds/{id}/result", func(r *operationRequest, a domain.Agent, q domain.SoftwareBuildResult) (domain.SoftwareBuild, error) {
		return s.Store.SubmitSoftwareBuild(r.Context(), a.ID, r.PathValue("id"), q)
	})
	deliveryBody(s, "POST /v1/software-releases", func(r *operationRequest, a domain.Agent, q domain.SoftwareReleaseRequest) (domain.SoftwareRelease, error) {
		return s.Store.CreateSoftwareRelease(r.Context(), a.ID, q, func(manifest domain.SoftwareManifest) (string, string, error) {
			if len(s.distributionKey) == 0 {
				return "", "", domain.Fail(domain.Prerequisite, "native distribution signer is not provisioned; internal publication remains disabled")
			}
			return software.SignRelease(manifest, s.distributionKey)
		})
	})
	read(s, "GET /v1/software-releases/{id}", func(r *operationRequest, a domain.Agent) (domain.SoftwareRelease, error) {
		if err := publicationRead(r); err != nil {
			return domain.SoftwareRelease{}, err
		}
		return s.Store.SoftwareRelease(r.Context(), a.ID, r.PathValue("id"))
	})
	read(s, "GET /v1/software-releases", func(r *operationRequest, a domain.Agent) (domain.Page[domain.SoftwareRelease], error) {
		if err := publicationRead(r, "product", "cursor", "limit"); err != nil {
			return domain.Page[domain.SoftwareRelease]{}, err
		}
		limit, err := queryLimit(r)
		if err != nil {
			return domain.Page[domain.SoftwareRelease]{}, err
		}
		return s.Store.SoftwareReleases(r.Context(), a.ID, r.URL.Query().Get("product"), r.URL.Query().Get("cursor"), limit)
	})
	deliveryBody(s, "POST /v1/software-releases/{id}/revoke", func(r *operationRequest, a domain.Agent, q domain.SoftwareReleaseChange) (domain.SoftwareRelease, error) {
		return s.Store.RevokeSoftwareRelease(r.Context(), a.ID, r.PathValue("id"), q)
	})
	deliveryBody(s, "POST /v1/software-observations", func(r *operationRequest, a domain.Agent, q domain.SoftwareObservationRequest) (domain.SoftwareInventory, error) {
		return s.Store.ObserveSoftware(r.Context(), a.ID, q)
	})
	read(s, "GET /v1/software-inventory", func(r *operationRequest, a domain.Agent) ([]domain.SoftwareInventory, error) {
		if err := publicationRead(r); err != nil {
			return nil, err
		}
		return s.Store.SoftwareInventory(r.Context(), a.ID)
	})
	deliveryBody(s, "POST /v1/software-consumers", func(r *operationRequest, a domain.Agent, q domain.SoftwareConsumerRequest) (domain.SoftwareConsumer, error) {
		return s.Store.BindSoftwareConsumer(r.Context(), a.ID, q)
	})
	read(s, "GET /v1/software-consumers", func(r *operationRequest, a domain.Agent) ([]domain.SoftwareConsumer, error) {
		if err := publicationRead(r, "project"); err != nil {
			return nil, err
		}
		return s.Store.SoftwareConsumers(r.Context(), a.ID, r.URL.Query().Get("project"))
	})
	deliveryBody(s, "POST /v1/software-preflights", func(r *operationRequest, a domain.Agent, q domain.SoftwarePreflightRequest) (domain.SoftwarePreflight, error) {
		return s.Store.RequestSoftwarePreflight(r.Context(), a.ID, q)
	})
	read(s, "GET /v1/software-preflights/{id}", func(r *operationRequest, a domain.Agent) (domain.SoftwarePreflight, error) {
		if err := publicationRead(r); err != nil {
			return domain.SoftwarePreflight{}, err
		}
		return s.Store.SoftwarePreflight(r.Context(), a.ID, r.PathValue("id"))
	})
	read(s, "GET /v1/software-preflight-requests", func(r *operationRequest, a domain.Agent) ([]domain.SoftwarePreflight, error) {
		if err := publicationRead(r); err != nil {
			return nil, err
		}
		return s.Store.SoftwarePreflightInbox(r.Context(), a.ID, false)
	})
	deliveryBody(s, "POST /v1/software-preflight-results", func(r *operationRequest, a domain.Agent, q domain.SoftwarePreflightReport) (domain.SoftwarePreflight, error) {
		return s.Store.ReportSoftwarePreflight(r.Context(), a.ID, false, q)
	})
	deliveryBody(s, "POST /v1/software-deployments", func(r *operationRequest, a domain.Agent, q domain.SoftwareAuthorizationRequest) (domain.SoftwareDeployment, error) {
		return s.Store.AuthorizeSoftware(r.Context(), a.ID, q, func(permit domain.SoftwarePermit) (string, error) {
			return software.SignPermit(permit, s.identity.key)
		})
	})
	deliveryBody(s, "POST /v1/software-maintenance", func(r *operationRequest, a domain.Agent, q domain.SoftwareMaintenanceRequest) (domain.SoftwareMaintenance, error) {
		return s.Store.PauseSoftwareMaintenance(r.Context(), a.ID, q)
	})
	read(s, "GET /v1/software-maintenance/{id}", func(r *operationRequest, a domain.Agent) (domain.SoftwareMaintenance, error) {
		if err := publicationRead(r); err != nil {
			return domain.SoftwareMaintenance{}, err
		}
		return s.Store.SoftwareMaintenance(r.Context(), a.ID, r.PathValue("id"))
	})
	read(s, "GET /v1/software-deployments/{id}", func(r *operationRequest, a domain.Agent) (domain.SoftwareDeployment, error) {
		if err := publicationRead(r); err != nil {
			return domain.SoftwareDeployment{}, err
		}
		return s.Store.SoftwareDeployment(r.Context(), a.ID, r.PathValue("id"))
	})
	read(s, "GET /v1/software-requests", func(r *operationRequest, a domain.Agent) ([]domain.SoftwareDeployment, error) {
		if err := publicationRead(r); err != nil {
			return nil, err
		}
		return s.Store.SoftwareInbox(r.Context(), a.ID, false)
	})
	deliveryBody(s, "POST /v1/software-results", func(r *operationRequest, a domain.Agent, q domain.SoftwareResultRequest) (domain.SoftwareDeployment, error) {
		return s.Store.ReportSoftware(r.Context(), a.ID, false, q)
	})
	deliveryBody(s, "POST /v1/software-leases", func(r *operationRequest, a domain.Agent, q domain.UpgradeLeaseRequest) (domain.UpgradeLease, error) {
		return s.Store.LeaseSoftware(r.Context(), a.ID, false, q, func(lease domain.UpgradeLease) (string, error) {
			return upgrade.SignLease(lease, s.identity.key)
		})
	})
}
