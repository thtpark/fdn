package remote

import (
	"strconv"
	"strings"

	"github.com/thtpark/fdn/internal/domain"
)

func processEffectiveUID(status []byte) (uint32, error) {
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "Uid:" {
			continue
		}
		if len(fields) != 5 {
			return 0, domain.Fail(domain.Prerequisite, "kernel process effective UID is malformed")
		}
		uid, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			return 0, err
		}
		return uint32(uid), nil
	}
	return 0, domain.Fail(domain.Prerequisite, "kernel process effective UID is unavailable")
}
