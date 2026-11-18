package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestSoftwarePreinstallRecoveryCLIRequiresExplicitExactOperatorInput(t *testing.T) {
	for _, args := range [][]string{
		{"software-recover"},
		{"software-recover", "--updater-policy", "missing"},
		{"software-recover", "--updater-policy", "missing", "--input", "missing"},
	} {
		var output, diagnostic bytes.Buffer
		code := RunService(context.Background(), args, bytes.NewReader(nil), &output, &diagnostic)
		var envelope struct {
			OK    bool          `json:"ok"`
			Error *domain.Fault `json:"error"`
		}
		if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
			t.Fatal(err, output.String(), diagnostic.String())
		}
		if code == 0 || envelope.OK || envelope.Error == nil || envelope.Error.Code != domain.Invalid {
			t.Fatal("operator recovery was not explicitly fenced before native I/O", code, envelope)
		}
	}
}

func TestSoftwareManagedRecoveryCLIRejectsUnknownAndChangedOperatorInput(t *testing.T) {
	for _, input := range []string{
		`{"stopped":true}`,
		`{"source":"caller-asserted"}`,
		`{"operation_id":"different"}`,
		`{"baseline_release_file":"/signed-baseline.json","expected_mode_revision":29,"force":true}`,
	} {
		t.Run(input, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "input.json")
			if err := os.WriteFile(path, []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			var output, diagnostic bytes.Buffer
			code := RunService(context.Background(), []string{"--operation-id", "exact", "software-recover", "--updater-policy", "missing", "--input", path},
				bytes.NewReader(nil), &output, &diagnostic)
			var envelope struct {
				OK    bool          `json:"ok"`
				Error *domain.Fault `json:"error"`
			}
			if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
				t.Fatal(err, output.String())
			}
			if code == 0 || envelope.OK || envelope.Error == nil ||
				(envelope.Error.Code != domain.Invalid && envelope.Error.Code != domain.Conflict) {
				t.Fatal("strict operator input reached native runtime", envelope, diagnostic.String())
			}
		})
	}
}
