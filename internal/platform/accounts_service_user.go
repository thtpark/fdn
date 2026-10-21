package platform

import (
	"errors"
	"os/user"
	"strconv"
)

type serviceUIDLookup func(string) (uint32, bool, error)

func serviceAccountUID(name string) (uint32, bool, error) {
	account, err := user.Lookup(name)
	var unknown user.UnknownUserError
	if errors.As(err, &unknown) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	return uint32(uid), err == nil, err
}
