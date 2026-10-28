package message

import (
	"bytes"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestAgentResetPreservesExistingReasonOnlyWireEnvelope(t *testing.T) {
	reset := domain.AgentResetRequest{Command: domain.Command{OperationID: "reset_wire"}, Reason: "Explicit runtime reset"}
	encoded, err := MarshalPayload(reset)
	if err != nil {
		t.Fatal(err)
	}
	existing, err := MarshalPayload(domain.DiagnosticRefreshRequest{Command: reset.Command, Reason: reset.Reason})
	if err != nil || !bytes.Equal(encoded, existing) {
		t.Fatal("reset unnecessarily changed the native payload type", err)
	}
	var decoded domain.AgentResetRequest
	if err := UnmarshalPayload(encoded, &decoded); err != nil || decoded != reset {
		t.Fatal("reset did not round-trip its exact bounded reason", decoded, err)
	}
}
