package process

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNativeCaptureFailureAndCancellation(t *testing.T) {
	buffer := NewBuffer(100)
	err := (Native{}).Run(context.Background(), Spec{Path: "/bin/sh", Args: []string{"-c", "printf hello"}, Stdout: buffer, Stderr: buffer, Env: Environment()})
	data, readErr := buffer.Bytes()
	if err != nil || readErr != nil || string(data) != "hello" {
		t.Fatal(string(data), err, readErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = (Native{}).Run(ctx, Spec{Path: "/bin/sh", Args: []string{"-c", "sleep 60 & wait"}, Env: Environment()})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Fatal("process tree not cancelled", err)
	}
	buffer = NewBuffer(2)
	if n, err := buffer.Write([]byte("large")); n != 5 || err != nil {
		t.Fatal(n, err)
	}
	if _, err := buffer.Bytes(); err == nil {
		t.Fatal("output overflow hidden")
	}
	t.Setenv("FDN_SECRET_TEST", "must-not-inherit")
	for _, item := range Environment() {
		if strings.Contains(item, "must-not-inherit") {
			t.Fatal("inherited secret environment")
		}
	}
}
