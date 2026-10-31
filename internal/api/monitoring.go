package api

import (
	"net/url"

	"github.com/thtpark/fdn/internal/domain"
)

func monitoringRead(r *operationRequest, audience bool) error {
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		return domain.Fail(domain.Invalid, "monitoring reads do not accept a body")
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return domain.Fail(domain.Invalid, "monitoring query is malformed")
	}
	for key, values := range query {
		if !audience || (key != "scope" && key != "project") || len(values) != 1 {
			return domain.Fail(domain.Invalid, "monitoring query contains unsupported or repeated selectors")
		}
	}
	return nil
}
