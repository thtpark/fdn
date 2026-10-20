package domain

import (
	"errors"
	"net/http"
)

type Code string

const (
	Invalid      Code = "invalid"
	NotFound     Code = "not-found"
	Denied       Code = "permission-denied"
	Conflict     Code = "conflict"
	Prerequisite Code = "prerequisite"
	Unavailable  Code = "unavailable"
	Provider     Code = "provider-failed"
	Internal     Code = "internal"
	Cancelled    Code = "cancelled"
)

type Fault struct {
	Code      Code   `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e *Fault) Error() string { return string(e.Code) + ": " + e.Message }

func Fail(code Code, message string) *Fault {
	return &Fault{Code: code, Message: message, Retryable: code == Unavailable}
}

func AsFault(err error) *Fault {
	var fault *Fault
	if errors.As(err, &fault) {
		return fault
	}
	return Fail(Internal, "operation failed; consult the local service diagnostics")
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	switch AsFault(err).Code {
	case Invalid:
		return 2
	case NotFound:
		return 3
	case Denied:
		return 4
	case Conflict:
		return 5
	case Prerequisite:
		return 6
	case Unavailable:
		return 7
	case Provider:
		return 8
	case Cancelled:
		return 130
	default:
		return 9
	}
}

func HTTPStatus(err error) int {
	switch AsFault(err).Code {
	case Invalid:
		return http.StatusBadRequest
	case NotFound:
		return http.StatusNotFound
	case Denied:
		return http.StatusForbidden
	case Conflict:
		return http.StatusConflict
	case Prerequisite:
		return http.StatusPreconditionFailed
	case Unavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
