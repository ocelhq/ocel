package manual

import (
	"context"
	"fmt"
	"strconv"

	"github.com/ocelhq/ocel/platform/vps/provider/listeners"
)

const DefaultPort = 8480

type Box interface {
	Listening(ctx context.Context) ([]listeners.Listener, error)
	Publishing(ctx context.Context, port string) ([]string, error)
	Claimed(ctx context.Context) ([]string, error)
	Probe(ctx context.Context, hostname string) (answered, failure string, err error)
}

type Manual struct {
	Box  Box
	Port int
}

func ForwardTo(port int) string { return "http://127.0.0.1:" + strconv.Itoa(port) }

func Route(hostname string, port int) string {
	return fmt.Sprintf("Route %s → %s (keep Host, set X-Forwarded-Proto)", hostname, ForwardTo(port))
}

func RouteOn(hostname string, port int, network, upstream string) string {
	return fmt.Sprintf("Route %s → %s on the %s network, or %s from the host (keep Host, set X-Forwarded-Proto)",
		hostname, upstream, network, ForwardTo(port))
}
