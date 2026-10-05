package host

import (
	"context"
	"strconv"

	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	storeBusyExit  = 75
	storeCallTries = 4
)

var storeCallBackoff = retryBackoff{base: 1, ceiling: 8, spread: 2}

var storeCurl = []string{"curl", "--silent", "--connect-timeout", "10", "--max-time", "60"}

func storeExec(store string) []string {
	return append([]string{"timeout", "-k", strconv.Itoa(killGraceSeconds), strconv.Itoa(storeCallSeconds),
		"docker", "exec", "--interactive", store}, storeCurl...)
}

func (h *Host) calledStore(ctx context.Context, what, script string, body []byte, elevation string) error {
	for try := 1; ; try++ {
		result, err := h.stream(ctx, script, fedBody(body), elevation)
		if err != nil {
			return err
		}
		if result.Code == 0 {
			return nil
		}
		if result.Code != storeBusyExit {
			return refusal.Refuse(refusal.CodeNotReady,
				"could not %s on %s: %v", what, h.named(), h.refuse(what, result, elevation))
		}
		if try == storeCallTries {
			return refusal.Refuse(refusal.CodeNotReady,
				"could not %s on %s: the store was busy or silent on all %d tries, the last saying: %s",
				what, h.named(), storeCallTries, spoken(result))
		}
		if err := h.pause(ctx, storeCallBackoff.after(try)); err != nil {
			return err
		}
	}
}
