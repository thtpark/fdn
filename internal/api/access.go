package api

import (
	"context"
	"net/http"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/ingress"
)

type accessKey struct{}
type accessRecord struct {
	id         string
	actor      string
	route      string
	generation int64
	owner      int64
}

type accessWriter struct {
	http.ResponseWriter
	status int
}

func (w *accessWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *accessWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *accessWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func recordActor(ctx context.Context, actor string, identity domain.CertificateIdentity) {
	if entry, ok := ctx.Value(accessKey{}).(*accessRecord); ok {
		entry.actor, entry.generation, entry.owner = actor, identity.Generation, identity.OwnerGeneration
	}
}
func recordRoute(r *http.Request) {
	if entry, ok := r.Context().Value(accessKey{}).(*accessRecord); ok {
		entry.route = r.Pattern
	}
}

func (s *Server) access(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entry := &accessRecord{id: domain.NewID("request"), route: "unmatched"}
		w.Header().Set("X-Fdn-Request-ID", entry.id)
		writer := &accessWriter{ResponseWriter: w}
		request := r.WithContext(context.WithValue(r.Context(), accessKey{}, entry))
		defer func() {
			status := writer.status
			if status == 0 {
				status = http.StatusOK
			}
			outcome := "completed"
			if status == http.StatusUnauthorized || status == http.StatusForbidden {
				outcome = "denied"
			} else if status >= 500 {
				outcome = "error"
			}
			fields := []any{"request_id", entry.id, "actor_id", entry.actor, "key_generation", entry.generation, "owner_generation", entry.owner, "method", r.Method, "route", entry.route, "status", status, "outcome", outcome}
			if uid, ok := ingress.UID(r.Context()); ok {
				fields = append(fields, "peer_uid", uid)
			}
			s.Log.Info("Foundation request", fields...)
		}()
		next.ServeHTTP(writer, request)
	})
}
