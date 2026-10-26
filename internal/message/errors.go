package message

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message/wire"
	"google.golang.org/protobuf/proto"
)

func invalid(text string) error     { return domain.Fail(domain.Invalid, text) }
func unavailable(text string) error { return domain.Fail(domain.Unavailable, text) }

var faultCodes = map[domain.Code]wire.Fault_Code{
	domain.Invalid: wire.Fault_INVALID, domain.NotFound: wire.Fault_NOT_FOUND,
	domain.Denied: wire.Fault_DENIED, domain.Conflict: wire.Fault_CONFLICT,
	domain.Prerequisite: wire.Fault_PREREQUISITE, domain.Unavailable: wire.Fault_UNAVAILABLE,
	domain.Provider: wire.Fault_PROVIDER, domain.Internal: wire.Fault_INTERNAL,
	domain.Cancelled: wire.Fault_CANCELLED,
}

type publicError struct {
	cause error
	fault domain.Fault
}

func (e *publicError) Error() string { return e.cause.Error() }
func (e *publicError) Unwrap() error { return e.cause }

func validPublicMessage(message string) bool {
	return len(message) <= MaxPublicErrorMessageBytes && utf8.ValidString(message) && !strings.ContainsRune(message, 0)
}

// PublicError explicitly approves a recognized domain.Fault's current message
// for transmission. The caller must establish that it is already public; this
// helper does not sanitize private text. It snapshots the fault without copying
// any surrounding error text. Nil stays nil. Non-domain faults, unknown codes,
// and oversized/invalid messages retain the default wire redaction.
func PublicError(err error) error {
	if err == nil {
		return nil
	}
	var public *publicError
	if errors.As(err, &public) && public != nil {
		return err
	}
	var fault *domain.Fault
	if !errors.As(err, &fault) || fault == nil || !validPublicMessage(fault.Message) {
		return err
	}
	if _, known := faultCodes[fault.Code]; !known {
		return err
	}
	return &publicError{cause: err, fault: *fault}
}

func safeFault(err error) *wire.Fault {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &wire.Fault{Code: wire.Fault_CANCELLED}
	}
	var public *publicError
	if errors.As(err, &public) && public != nil && validPublicMessage(public.fault.Message) {
		if code, known := faultCodes[public.fault.Code]; known {
			return &wire.Fault{Code: code, Retryable: public.fault.Retryable, Message: proto.String(public.fault.Message)}
		}
	}
	fault := domain.AsFault(err)
	if fault == nil {
		return &wire.Fault{Code: wire.Fault_INTERNAL}
	}
	code, ok := faultCodes[fault.Code]
	if !ok {
		code = wire.Fault_INTERNAL
	}
	return &wire.Fault{Code: code, Retryable: fault.Retryable}
}

func fromFault(fault *wire.Fault) error {
	if fault == nil || fault.Code == wire.Fault_UNSPECIFIED {
		return invalid("invalid message fault")
	}
	if fault.Message != nil && !validPublicMessage(fault.GetMessage()) {
		return invalid("invalid public message fault")
	}
	for code, wireCode := range faultCodes {
		if wireCode == fault.Code {
			message := "remote message operation " + string(code)
			if fault.Message != nil {
				message = fault.GetMessage()
			}
			return &domain.Fault{Code: code, Retryable: fault.Retryable, Message: message}
		}
	}
	return invalid("unknown message fault")
}
