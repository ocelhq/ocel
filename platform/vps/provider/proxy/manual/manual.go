package manual

import (
	"context"
	"fmt"
	"strconv"

	"github.com/ocelhq/ocel/platform/vps/provider/listeners"
)

const DefaultPort = 8480

const httpsPort = 443

type Box interface {
	Listening(ctx context.Context) ([]listeners.Listener, error)
	Publishing(ctx context.Context, port string) ([]string, error)
	Claimed(ctx context.Context) ([]string, error)
	Probe(ctx context.Context, hostname string) (answered, unreached string, err error)
}

type Manual struct {
	Box  Box
	Port int
}

func Loopback(port int) string { return "http://127.0.0.1:" + strconv.Itoa(port) }

func Route(hostname string, port int) string {
	return fmt.Sprintf("Route %s → %s (keep Host, set X-Forwarded-Proto)", hostname, Loopback(port))
}
