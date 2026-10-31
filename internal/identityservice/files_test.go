package identityservice

import (
	"net/http"
	"net/url"
	"testing"
)

func TestFilePublicationProxyKeepsNarrowMethodsQueriesAndDepth(t *testing.T) {
	for _, item := range []struct {
		method, path string
		allowed      bool
	}{
		{"GET", "/v1/files?cursor=opaque&limit=10&include_deleted=false", true},
		{"GET", "/v1/files/file_fixture", true},
		{"GET", "/v1/files/file_fixture/chunks?offset=262144", true},
		{"POST", "/v1/files", true},
		{"POST", "/v1/files/file_fixture/chunks", true},
		{"POST", "/v1/files/file_fixture/finalize", true},
		{"POST", "/v1/files/file_fixture/publish", true},
		{"POST", "/v1/files/file_fixture/grants", true},
		{"POST", "/v1/files/file_fixture/delete", true},
		{"POST", "/v1/files/file_fixture/downloaded", true},
		{"GET", "/v1/publications?kind=skill&scope=project&target=project_fixture&limit=10", true},
		{"GET", "/v1/publications/publication_fixture/versions?limit=10", true},
		{"GET", "/v1/publication-versions/publication_version_fixture", true},
		{"POST", "/v1/publications/publication_fixture/change", true},
		{"GET", "/v1/files?actor=agent_other", false},
		{"GET", "/v1/files/file_fixture?owner=true", false},
		{"GET", "/v1/files/file_fixture/chunks?offset=0&offset=1", false},
		{"POST", "/v1/files/file_fixture/chunks?offset=0", false},
		{"POST", "/v1/files/file_fixture/grants?founder=true", false},
		{"GET", "/v1/files/file_fixture/delete", false},
		{"GET", "/v1/files/file_fixture/grants", false},
		{"DELETE", "/v1/files/file_fixture", false},
		{"PUT", "/v1/files/file_fixture/chunks", false},
		{"GET", "/v1/files/file_fixture/chunks/private", false},
		{"GET", "/v1/publications?private=true", false},
		{"POST", "/v1/publications/publication_fixture/change?force=true", false},
		{"GET", "/v1/publications/publication_fixture/versions?actor=other", false},
		{"POST", "/v1/publication-versions/publication_version_fixture", false},
		{"GET", "/v1/files/../identity/owner", false},
	} {
		parsed, err := url.Parse(item.path)
		if err != nil {
			t.Fatal(err)
		}
		if got := ordinaryPath(item.method, parsed); got != item.allowed {
			t.Fatalf("%s %s: allowed=%v, want=%v", item.method, item.path, got, item.allowed)
		}
	}
	if ordinaryPath(http.MethodGet, &url.URL{Scheme: "https", Host: "elsewhere", Path: "/v1/files"}) {
		t.Fatal("file proxy accepted an arbitrary origin")
	}
}
