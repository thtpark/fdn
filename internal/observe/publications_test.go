package observe

import (
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestFilePublicationObservationPrivacyAndNoByteSubscriptions(t *testing.T) {
	queries := []Query{{Kind: "files"}, {Kind: "file", ID: "file_fixture"}, {Kind: "publications"}, {Kind: "publicationVersions", ID: "publication_fixture"}}
	private, public, err := Filters(queries, "agent_fixture")
	if err != nil || !private.Matches(domain.Update{Topic: "file", SourceID: "file_fixture"}, "agent_fixture") ||
		!public.Matches(domain.Update{Topic: "publication", SourceID: "publication_fixture"}, "agent_fixture") {
		t.Fatal("observer omitted applicable private/shared file or publication feed", private, public, err)
	}
	if !queries[0].Matches(domain.Update{Topic: "file"}, "") || !queries[3].Matches(domain.Update{Topic: "publication", SourceID: "publication_fixture"}, "") {
		t.Fatal("file/publication changes did not invalidate matching views")
	}
	for _, q := range []Query{{Kind: "fileChunk", ID: "file_fixture"}, {Kind: "publicationVersion", ID: "publication_version_fixture"}} {
		if err := q.Validate(); err != nil || q.Subscribable() || q.SampleInterval() != 0 {
			t.Fatal("immutable bytes/versions became sampled or subscribed", q, err)
		}
	}
}
