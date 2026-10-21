//go:build !linux && !darwin

package ingress

import "os"

func interruptibleProxyFile(*os.File) (*os.File, error) { return nil, ErrNoPeerUID }
