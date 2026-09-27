package consent

import (
	"fmt"
	"os"
	"strings"
)

const BypassEnv = "OCEL_DESTROY_BYPASS_CONFIRMATION"

type Bypass struct {
	Noun          string
	Subject       string
	Action        string
	Verb          string
	Yes           bool
	Dry           bool
	GrantsWhenDry bool
	TTY           bool
}

func (b Bypass) Granted() (granted bool, notice string, err error) {
	requested := strings.TrimSpace(os.Getenv(BypassEnv))
	switch {
	case b.Dry:
		return b.GrantsWhenDry && requested == b.Subject, "", nil
	case requested == b.Subject:
		return true, fmt.Sprintf("%s=%s: %s without confirmation", BypassEnv, b.Subject, b.Action), nil
	case requested == "" || b.Yes:
	case !b.TTY:
		return false, "", fmt.Errorf("%s is set to %q, but this %s is %q; it must name the %s being %s",
			BypassEnv, requested, b.Noun, b.Subject, b.Noun, b.Verb)
	default:
		return false, fmt.Sprintf("%s is set to %q, not this %s (%s); confirming interactively instead",
			BypassEnv, requested, b.Noun, b.Subject), nil
	}
	return false, "", nil
}
