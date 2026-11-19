package identityservice

import (
	"net/url"
	"testing"
)

func TestOrdinaryProxyAdmitsUpstreamImportWithoutQueryOrTraversal(t *testing.T) {
	for _, test := range []struct {
		method, path string
		allowed      bool
	}{
		{"POST", "/v1/repositories/repository_fixture/upstream", true},
		{"POST", "/v1/repositories/repository_fixture/upstream?force=1", false},
		{"GET", "/v1/repositories/repository_fixture/upstream", false},
		{"POST", "/v1/repositories/repository_fixture/upstream/extra", false},
		{"POST", "/v1/repositories/../upstream", false},
	} {
		parsed, err := url.Parse(test.path)
		if err != nil {
			t.Fatal(err)
		}
		if ordinaryPath(test.method, parsed) != test.allowed {
			t.Fatal("unexpected proxy decision", test.method, test.path, test.allowed)
		}
	}
}
