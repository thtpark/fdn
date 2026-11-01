package api

import (
	"net/url"
	"strconv"

	"github.com/thtpark/fdn/internal/domain"
)

func publicationRead(r *operationRequest, names ...string) error {
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		return domain.Fail(domain.Invalid, "file and publication reads must be bodyless")
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return domain.Fail(domain.Invalid, "invalid file/publication query")
	}
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		allowed[name] = true
	}
	for name, values := range query {
		if !allowed[name] || len(values) != 1 {
			return domain.Fail(domain.Invalid, "unsupported or repeated file/publication query field")
		}
	}
	return nil
}

func publicationBool(r *operationRequest, name string) (bool, error) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return false, nil
	}
	if value != "true" && value != "false" {
		return false, domain.Fail(domain.Invalid, name+" must be true or false")
	}
	return value == "true", nil
}

func (s *Server) publicationRoutes() {
	body(s, "POST /v1/files", false, func(r *operationRequest, a domain.Agent, q domain.FileUploadRequest) (domain.SharedFile, error) {
		return s.Store.BeginFile(r.Context(), a.ID, q)
	})
	body(s, "POST /v1/files/{id}/chunks", false, func(r *operationRequest, a domain.Agent, q domain.FileChunkRequest) (domain.FileProgress, error) {
		return s.Store.PutFileChunk(r.Context(), a.ID, r.PathValue("id"), q)
	})
	body(s, "POST /v1/files/{id}/finalize", false, func(r *operationRequest, a domain.Agent, q domain.FileChangeRequest) (domain.SharedFile, error) {
		return s.Store.FinalizeFile(r.Context(), a.ID, r.PathValue("id"), q)
	})
	body(s, "POST /v1/files/{id}/publish", false, func(r *operationRequest, a domain.Agent, q domain.FileChangeRequest) (domain.SharedFile, error) {
		return s.Store.PublishFile(r.Context(), a.ID, r.PathValue("id"), q)
	})
	body(s, "POST /v1/files/{id}/grants", false, func(r *operationRequest, a domain.Agent, q domain.FileGrantRequest) (domain.SharedFile, error) {
		return s.Store.GrantFile(r.Context(), a.ID, r.PathValue("id"), q)
	})
	body(s, "POST /v1/files/{id}/delete", false, func(r *operationRequest, a domain.Agent, q domain.FileChangeRequest) (domain.SharedFile, error) {
		return s.Store.DeleteFile(r.Context(), a.ID, r.PathValue("id"), q)
	})
	body(s, "POST /v1/files/{id}/downloaded", false, func(r *operationRequest, a domain.Agent, q domain.FileDownloadRequest) (domain.FileDownloadReceipt, error) {
		return s.Store.AcknowledgeFileDownload(r.Context(), a.ID, r.PathValue("id"), q)
	})
	read(s, "GET /v1/files", func(r *operationRequest, a domain.Agent) (domain.Page[domain.SharedFile], error) {
		var zero domain.Page[domain.SharedFile]
		if err := publicationRead(r, "cursor", "limit", "include_deleted"); err != nil {
			return zero, err
		}
		limit, err := queryLimit(r)
		if err != nil {
			return zero, err
		}
		deleted, err := publicationBool(r, "include_deleted")
		if err != nil {
			return zero, err
		}
		return s.Store.Files(r.Context(), a.ID, r.URL.Query().Get("cursor"), limit, deleted)
	})
	read(s, "GET /v1/files/{id}", func(r *operationRequest, a domain.Agent) (domain.SharedFile, error) {
		if err := publicationRead(r); err != nil {
			return domain.SharedFile{}, err
		}
		return s.Store.File(r.Context(), a.ID, r.PathValue("id"))
	})
	read(s, "GET /v1/files/{id}/chunks", func(r *operationRequest, a domain.Agent) (domain.FileChunk, error) {
		if err := publicationRead(r, "offset"); err != nil {
			return domain.FileChunk{}, err
		}
		offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
		if err != nil {
			return domain.FileChunk{}, domain.Fail(domain.Invalid, "an integer chunk offset is required")
		}
		return s.Store.FileChunk(r.Context(), a.ID, r.PathValue("id"), offset)
	})
	body(s, "POST /v1/publications", false, func(r *operationRequest, a domain.Agent, q domain.PublicationRequest) (domain.Publication, error) {
		return s.Store.CreatePublication(r.Context(), a.ID, q)
	})
	body(s, "POST /v1/publications/{id}/change", false, func(r *operationRequest, a domain.Agent, q domain.PublicationChange) (domain.Publication, error) {
		return s.Store.ChangePublication(r.Context(), a.ID, r.PathValue("id"), q)
	})
	read(s, "GET /v1/publications/{id}", func(r *operationRequest, a domain.Agent) (domain.Publication, error) {
		if err := publicationRead(r); err != nil {
			return domain.Publication{}, err
		}
		return s.Store.Publication(r.Context(), a.ID, r.PathValue("id"))
	})
	read(s, "GET /v1/publication-versions/{id}", func(r *operationRequest, a domain.Agent) (domain.PublicationVersion, error) {
		if err := publicationRead(r); err != nil {
			return domain.PublicationVersion{}, err
		}
		return s.Store.PublicationVersion(r.Context(), a.ID, r.PathValue("id"))
	})
	read(s, "GET /v1/publications/{id}/versions", func(r *operationRequest, a domain.Agent) (domain.Page[domain.PublicationVersion], error) {
		if err := publicationRead(r, "cursor", "limit"); err != nil {
			return domain.Page[domain.PublicationVersion]{}, err
		}
		limit, err := queryLimit(r)
		if err != nil {
			return domain.Page[domain.PublicationVersion]{}, err
		}
		return s.Store.PublicationVersions(r.Context(), a.ID, r.PathValue("id"), r.URL.Query().Get("cursor"), limit)
	})
	read(s, "GET /v1/publications", func(r *operationRequest, a domain.Agent) (domain.Page[domain.Publication], error) {
		var zero domain.Page[domain.Publication]
		if err := publicationRead(r, "kind", "scope", "target", "cursor", "limit", "include_archived"); err != nil {
			return zero, err
		}
		limit, err := queryLimit(r)
		if err != nil {
			return zero, err
		}
		archived, err := publicationBool(r, "include_archived")
		if err != nil {
			return zero, err
		}
		query := r.URL.Query()
		return s.Store.Publications(r.Context(), a.ID, query.Get("kind"), query.Get("scope"), query.Get("target"), query.Get("cursor"), limit, archived)
	})
}
