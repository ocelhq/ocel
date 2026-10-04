package consent

import (
	"fmt"
	"os"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/clierror"
)

const BypassEnv = "OCEL_DESTROY_BYPASS_CONFIRMATION"

type Bypass struct {
	Noun         string
	Subject      string
	Action       string
	Verb         string
	Yes          bool
	DryRun       bool
	GrantsDryRun bool
	TTY          bool
}

func (b Bypass) Granted() (granted bool, notice string, err error) {
	requested := strings.TrimSpace(os.Getenv(BypassEnv))
	switch {
	case b.DryRun:
		return b.GrantsDryRun && requested == b.Subject, "", nil
	case requested == b.Subject:
		return true, fmt.Sprintf("%s=%s: %s without confirmation", BypassEnv, b.Subject, b.Action), nil
	case requested == "" || b.Yes:
	case !b.TTY:
		return false, "", clierror.NewConfirmationBypassMismatch(
			fmt.Errorf("%s is set to %q, but this %s is %q; it must name the %s being %s",
				BypassEnv, requested, b.Noun, b.Subject, b.Noun, b.Verb),
			fmt.Sprintf("set %s to %s", BypassEnv, b.Subject),
		)
	default:
		return false, fmt.Sprintf("%s is set to %q, not this %s (%s); confirming interactively instead",
			BypassEnv, requested, b.Noun, b.Subject), nil
	}
	return false, "", nil
}
