package identityservice

import (
	"context"
	"net"
	"testing"

	"github.com/thtpark/fdn/internal/api"
	"github.com/thtpark/fdn/internal/domain"
)

func TestEmbeddedAttachmentUsesPrivateCapabilityAndExistingProxyChecks(t *testing.T) {
	f := newFixture(t)
	f.profile.Identity.DeviceID = domain.NewID("device")
	f.remote.profile = f.profile
	attachment, err := NewEmbeddedAttachment(f.config)
	if err != nil {
		t.Fatal(err)
	}
	service, stop := startService(t, f, attachment)
	defer stop()
	client := api.NewMessageClient("embedded://identity.local", attachment.Dial)
	client.Local = true
	defer client.Close()
	status, err := client.LocalStatus(context.Background())
	if err != nil || !status.Ready || !status.Agent.Founder || status.Identity.DeviceID != f.profile.Identity.DeviceID {
		t.Fatal("embedded attachment lost its actual Founder device binding", status, err)
	}
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	if _, err := attachment.Peer(left); domain.AsFault(err).Code != domain.Denied {
		t.Fatal("an unrelated in-process connection forged the identity capability", err)
	}
	for _, path := range []string{"/v1/identity/activate", "/v1/identity/owner", "/local/control", "/v1/../identity/owner"} {
		if err := client.Request(context.Background(), "POST", path, nil, new(any)); err == nil {
			t.Fatal("embedded attachment bypassed proxy route restrictions", path)
		}
	}
	stop()
	if service.Status().Ready {
		t.Fatal("foreground owner remained ready after joined shutdown")
	}
	if _, err := attachment.Dial(context.Background()); err == nil {
		t.Fatal("stopped embedded attachment could be reused")
	}
}
