package relay

import (
	"context"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const (
	Path        = "/forward"
	TargetParam = "to"
	AllowedEnv  = "OCEL_BASTION_ALLOWED"

	messageLimit = 1 << 20
	dialTimeout  = 10 * time.Second
)

func ParseAllowed(written string) []string {
	var allowed []string
	for _, target := range strings.Split(written, ",") {
		if target = strings.TrimSpace(target); target != "" {
			allowed = append(allowed, target)
		}
	}
	return allowed
}

func FormatAllowed(allowed []string) string { return strings.Join(allowed, ",") }

func NewHandler(allowed []string) http.Handler {
	allowed = slices.Clone(allowed)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != Path {
			http.NotFound(w, r)
			return
		}
		target := r.URL.Query().Get(TargetParam)
		if !slices.Contains(allowed, target) {
			http.Error(w, "the relay forwards to this environment's databases and caches alone", http.StatusForbidden)
			return
		}
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "the relay speaks websocket alone", http.StatusUpgradeRequired)
			return
		}
		dialed, err := dial(r.Context(), target)
		if err != nil {
			http.Error(w, "the relay could not reach the target: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer dialed.Close()
		socket, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		socket.SetReadLimit(messageLimit)
		pipe(dialed, websocket.NetConn(r.Context(), socket, websocket.MessageBinary))
	})
}

func dial(ctx context.Context, target string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	return (&net.Dialer{}).DialContext(ctx, "tcp", target)
}

func pipe(a, b net.Conn) {
	ended := make(chan struct{}, 2)
	for _, direction := range [][2]net.Conn{{a, b}, {b, a}} {
		go func() {
			_, _ = io.Copy(direction[0], direction[1])
			ended <- struct{}{}
		}()
	}
	<-ended
	_ = a.Close()
	_ = b.Close()
	<-ended
}
