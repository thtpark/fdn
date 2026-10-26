package message

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message/wire"
	"google.golang.org/protobuf/proto"
)

func TestPublicErrorPreservesOnlyApprovedDomainFault(t *testing.T) {
	original := &domain.Fault{Code: domain.Denied, Message: "fixture server refusal", Retryable: true}
	approved := PublicError(original)
	if !errors.Is(approved, original) || domain.AsFault(approved) != original {
		t.Fatal("public annotation lost the local error chain")
	}
	if PublicError(approved) != approved {
		t.Fatal("public annotation is not idempotent")
	}
	for _, err := range []error{
		approved,
		fmt.Errorf("private wrapper: %w", approved),
		PublicError(fmt.Errorf("private wrapper: %w", original)),
	} {
		fault := safeFault(err)
		if fault.Code != wire.Fault_DENIED || !fault.Retryable || fault.Message == nil ||
			fault.GetMessage() != original.Message {
			t.Fatalf("approved domain fault not preserved: %v", fault)
		}
		received := fromFault(fault)
		if received.Error() != "permission-denied: fixture server refusal" || !domain.AsFault(received).Retryable {
			t.Fatalf("public wire fault changed: %v", received)
		}
	}
	if safeFault(original).Message != nil {
		t.Fatal("merely being a domain fault must not expose its message")
	}
	if PublicError(nil) != nil {
		t.Fatal("nil annotation became an error")
	}
}

func TestPublicErrorSnapshotsApproval(t *testing.T) {
	original := &domain.Fault{Code: domain.Denied, Message: "approved message", Retryable: true}
	approved := PublicError(original)
	original.Code, original.Message, original.Retryable = domain.Internal, "private later mutation", false
	fault := safeFault(approved)
	if fault.Code != wire.Fault_DENIED || fault.GetMessage() != "approved message" || !fault.Retryable {
		t.Fatalf("later mutation changed approval: %v", fault)
	}
}

func TestPublicErrorBoundsAndDefaultRedaction(t *testing.T) {
	var typedNil *domain.Fault
	for _, err := range []error{
		errors.New("private backend secret"),
		typedNil,
		&domain.Fault{Code: domain.Code("unknown-private-code"), Message: "private message"},
		&domain.Fault{Code: domain.Denied, Message: strings.Repeat("s", MaxPublicErrorMessageBytes+1)},
		&domain.Fault{Code: domain.Denied, Message: "private\x00message"},
		&domain.Fault{Code: domain.Denied, Message: "\xff"},
	} {
		fault := safeFault(PublicError(err))
		if fault.Message != nil {
			t.Fatalf("unsafe public annotation exposed message: %v", fault)
		}
	}
	for _, message := range []string{"", strings.Repeat("x", MaxPublicErrorMessageBytes), "public Unicode: 승인 거부"} {
		fault := safeFault(PublicError(domain.Fail(domain.Denied, message)))
		if fault.Message == nil || domain.AsFault(fromFault(fault)).Message != message {
			t.Fatal("valid bounded public message lost its exact presence/value")
		}
	}
	for _, message := range []string{strings.Repeat("x", MaxPublicErrorMessageBytes+1), "bad\x00message", "\xff"} {
		requireCode(t, fromFault(&wire.Fault{Code: wire.Fault_DENIED, Message: proto.String(message)}), domain.Invalid)
	}
}
