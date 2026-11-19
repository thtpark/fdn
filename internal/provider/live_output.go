package provider

import (
	"github.com/thtpark/fdn/internal/process"
)

type invocationOutput struct {
	*process.PrivateOutput
}

func newInvocationOutput(path string, limit int) (*invocationOutput, error) {
	output, err := process.NewPrivateOutput(path, limit)
	if err != nil {
		return nil, err
	}
	return &invocationOutput{output}, nil
}

func (output *invocationOutput) finish() ([]byte, error) {
	return output.Finish()
}
