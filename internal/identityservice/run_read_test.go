package identityservice

import (
	"net/http"
	"testing"
)

func TestRunLookupProxyAllowsOnlyTheExactReadPath(t *testing.T) {
	target := mustRequest(t, "/v1/runs/run_fixture").URL
	if !ordinaryPath(http.MethodGet, target) {
		t.Fatal("exact run read is unavailable through the identity proxy")
	}
	for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodPut} {
		if ordinaryPath(method, target) {
			t.Fatal("run lookup admitted a mutation", method)
		}
	}
	for _, path := range []string{"/v1/runs/run_fixture/extra", "/v1/runs", "/v1/runs/run_fixture/", "/v1/runs/../me"} {
		if ordinaryPath(http.MethodGet, mustRequest(t, path).URL) {
			t.Fatal("run lookup admitted an expanded path", path)
		}
	}
}
