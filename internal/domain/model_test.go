package domain

import (
	"errors"
	"testing"
)

func TestTokensAndErrors(t *testing.T) {
	token := NewToken()
	if len(token) != 43 || token == NewToken() || !ValidHash(Hash(token)) {
		t.Fatal("invalid random token")
	}
	if ValidHash("short") || ValidHash("AA"+Hash(token)[2:]) {
		t.Fatal("accepted malformed hash")
	}
	if NewID("task") == NewID("task") {
		t.Fatal("duplicate ID")
	}
	for code, exit := range map[Code]int{Invalid: 2, NotFound: 3, Denied: 4, Conflict: 5, Prerequisite: 6, Unavailable: 7, Provider: 8, Internal: 9, Cancelled: 130} {
		err := Fail(code, "test")
		if ExitCode(err) != exit || AsFault(err).Code != code || err.Error() == "" {
			t.Fatal(code)
		}
	}
	if ExitCode(nil) != 0 || AsFault(errors.New("secret")).Message == "secret" {
		t.Fatal("error redaction")
	}
	if !ValidOperation("op_123") || ValidOperation("../bad") {
		t.Fatal("operation validation")
	}
}

func TestValidation(t *testing.T) {
	for _, p := range []RetryPolicy{{-1, 1}, {11, 1}, {1, 0}, {1, 86401}} {
		if p.Validate() == nil {
			t.Fatal(p)
		}
	}
	if (RetryPolicy{3, 30}).Validate() != nil {
		t.Fatal("valid policy")
	}
	if CheckText("", "x", 3, true) == nil || CheckText("abcd", "x", 3, false) == nil || CheckText("\x00", "x", 3, false) == nil {
		t.Fatal("text validation")
	}
	if CheckText("ok", "x", 3, true) != nil {
		t.Fatal("valid text")
	}
	if !Done.Terminal() || Running.Terminal() {
		t.Fatal("terminal states")
	}
}
