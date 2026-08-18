package edge

import (
	"fmt"
	"strings"
	"time"
)

type Outstanding struct {
	Kind string
	Name string
}

type OutstandingError struct {
	Because string
	Waited  time.Duration
	Items   []Outstanding
}

func (e *OutstandingError) Error() string {
	var b strings.Builder
	b.WriteString(e.Because)
	fmt.Fprintf(&b, ". Nothing is lost: this run gave up after about %s — re-run the same command and it will pick up where this one stopped, skipping everything already gone. Still standing:", e.Waited)
	for _, item := range e.Items {
		fmt.Fprintf(&b, "\n  • %s %s", item.Kind, item.Name)
	}
	return b.String()
}
