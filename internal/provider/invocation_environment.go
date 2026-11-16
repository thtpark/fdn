package provider

import (
	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/process"
)

func invocationEnvironment(c config.Worker, invocation Invocation) []string {
	environment := process.Environment()
	if !invocation.Coding {
		return environment
	}
	if c.SourcePath() != "" {
		environment = append(environment, config.WorkerConfigEnvironment+"="+c.SourcePath())
	}
	if invocation.TaskID != "" && invocation.RunID != "" {
		environment = append(environment, "FDN_WORK_TASK="+invocation.TaskID, "FDN_WORK_RUN="+invocation.RunID)
	}
	return environment
}
