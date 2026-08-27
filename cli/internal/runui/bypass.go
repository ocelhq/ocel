package runui

import (
	"fmt"
	"io"
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

func (b Bypass) Granted(stderr io.Writer) (bool, error) {
	requested := strings.TrimSpace(os.Getenv(BypassEnv))
	switch {
	case b.Dry:
		return b.GrantsWhenDry && requested == b.Subject, nil
	case requested == b.Subject:
		fmt.Fprintf(stderr, "%s=%s: %s without confirmation\n", BypassEnv, b.Subject, b.Action)
		return true, nil
	case requested == "" || b.Yes:
	case !b.TTY:
		return false, fmt.Errorf("%s is set to %q, but this %s is %q; it must name the %s being %s",
			BypassEnv, requested, b.Noun, b.Subject, b.Noun, b.Verb)
	default:
		fmt.Fprintf(stderr, "%s is set to %q, not this %s (%s); confirming interactively instead\n",
			BypassEnv, requested, b.Noun, b.Subject)
	}
	return false, nil
}
