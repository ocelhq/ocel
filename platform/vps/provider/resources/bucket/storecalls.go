package bucket

import (
	"context"
	"strconv"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const (
	storeBusyExit  = 75
	storeCallTries = 4
)

type objectStore struct{ *ssh.Box }

var storeCallBackoff = shell.Backoff{Base: 1, Ceiling: 8, Spread: 2}

var storeCurl = []string{"curl", "--silent", "--connect-timeout", "10", "--max-time", "60"}

func storeExec(store string) []string {
	return append([]string{"timeout", "-k", strconv.Itoa(shell.KillGraceSeconds), strconv.Itoa(shell.StoreCallSeconds),
		"docker", "exec", "--interactive", store}, storeCurl...)
}

func (o objectStore) calledStore(ctx context.Context, what, script string, body []byte, elevation ssh.Acting) error {
	for try := 1; ; try++ {
		result, err := o.Stream(ctx, elevation, script, fedBody(body))
		if err != nil {
			return err
		}
		if result.Code == 0 {
			return nil
		}
		if result.Code != storeBusyExit {
			return refusal.Refuse(refusal.CodeNotReady,
				"could not %s on %s: %v", what, o.Named(), o.Refuse(what, result))
		}
		if try == storeCallTries {
			return refusal.Refuse(refusal.CodeNotReady,
				"could not %s on %s: the store was busy or silent on all %d tries, the last saying: %s",
				what, o.Named(), storeCallTries, ssh.Reason(result))
		}
		if err := o.Wait(ctx, storeCallBackoff.After(try)); err != nil {
			return err
		}
	}
}
