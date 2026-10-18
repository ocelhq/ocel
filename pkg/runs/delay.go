package runs

import (
	"fmt"
	"time"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/provider"
)

const day = 24 * time.Hour

func (e *Engine) refuseDelay(name string, topic *provider.TopicSpec, due, now time.Time) error {
	limit := e.cfg.MaxOrderedDelay
	if limit <= 0 || !topic.Ordered || !due.After(now.Add(limit)) {
		return nil
	}
	kind := "topic"
	if IsTask(topic) {
		kind = "task"
	}
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
		"%s %q is ordered, and an ordered topic or task here holds a message at most %s before it is due, but this one is due at %s: send it nearer its time, or declare %q unordered",
		kind, name, spoken(limit), due.UTC().Format(time.RFC3339), name))
}

func spoken(d time.Duration) string {
	if d >= day && d%day == 0 {
		return fmt.Sprintf("%d days", d/day)
	}
	return d.String()
}
