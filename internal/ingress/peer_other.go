//go:build !linux && !darwin

package ingress

func kernelPeerUID(int) (uint32, error) { return 0, ErrNoPeerUID }
