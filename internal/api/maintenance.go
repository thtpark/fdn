package api

import (
	"net/http"
	"strconv"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/ingress"
)

// MaintenanceHandler is local OS-operator recovery, never an SSH gateway target.
func (s *Server) MaintenanceHandler() http.Handler {
	return s.access(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Fdn-API-Version", strconv.Itoa(domain.APIVersion))
		w.Header().Set("Cache-Control", "no-store")
		uid, ok := ingress.UID(r.Context())
		if !ok || uid != 0 {
			s.respond(w, r, nil, "", domain.Fail(domain.Denied, "local root operator required"))
			return
		}
		if r.Method != "POST" || r.URL.Path != "/v1/identity/recover-founder" {
			s.respond(w, r, nil, "", domain.Fail(domain.NotFound, "unknown maintenance operation"))
			return
		}
		recordActor(r.Context(), "os-root", domain.CertificateIdentity{})
		var req domain.RecoverIdentityRequest
		op, err := readBody(w, r, &req)
		if err != nil {
			s.respond(w, r, nil, op, err)
			return
		}
		identity, err := s.Store.RecoverFounderOffline(r.Context(), req)
		if err == nil {
			identity, err = s.provisionIdentity(r.Context(), identity)
		}
		s.respond(w, r, identity, op, err)
	}))
}
