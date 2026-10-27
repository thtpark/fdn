package upgrade

import (
	"context"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
)

func TestNativeUpgradeInstalledFilesDoNotProveRunningServiceHealth(t *testing.T) {
	native, recorder, signer, key := nativeFixture(t)
	permit := preparePermit(t, native, recorder, signer, key)
	req := domain.UpgradeNativeRequest{Action: "prepare", ID: permit.Proposal.Challenge.ID, Permit: &permit}
	if _, err := native.Handle(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	native.CheckServices = func(context.Context) error {
		return domain.Fail(domain.Unavailable, "fixture worker exited after activation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	req.Action = "apply"
	if _, err := native.Handle(ctx, req); err == nil {
		t.Fatal("new executable files were treated as successful running services")
	}
	if !recorder.newSource {
		t.Fatal("fixture did not exercise the post-install verification boundary")
	}
	status, err := native.Handle(context.Background(), domain.UpgradeNativeRequest{Action: "status", ID: req.ID})
	if err != nil || status.State != "operator-required" || status.Code != "installing" {
		t.Fatal("failed runtime verification was projected as a completed upgrade", status, err)
	}
}
