package relay

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
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

type Destination struct {
	Network netip.Prefix
	Port    uint16
}

func (d Destination) String() string {
	return d.Network.String() + ":" + strconv.Itoa(int(d.Port))
}

func (d Destination) admits(target netip.AddrPort) bool {
	return target.Port() == d.Port && d.Network.Contains(target.Addr())
}

func ParseDestinations(written string) ([]Destination, error) {
	var destinations []Destination
	for _, entry := range strings.Split(written, ",") {
		if entry = strings.TrimSpace(entry); entry == "" {
			continue
		}
		at := strings.LastIndex(entry, ":")
		if at < 0 || !strings.Contains(entry[:at], "/") {
			return nil, fmt.Errorf("%q names no address range and port, written as 10.240.0.0/20:5432", entry)
		}
		network, port := entry[:at], entry[at+1:]
		prefix, err := netip.ParsePrefix(network)
		if err != nil {
			return nil, fmt.Errorf("%q names no address range: %w", entry, err)
		}
		number, err := strconv.ParseUint(port, 10, 16)
		if err != nil {
			return nil, fmt.Errorf("%q names no port: %w", entry, err)
		}
		destinations = append(destinations, Destination{Network: prefix.Masked(), Port: uint16(number)})
	}
	return destinations, nil
}

func FormatDestinations(destinations []Destination) string {
	written := make([]string, len(destinations))
	for i, destination := range destinations {
		written[i] = destination.String()
	}
	return strings.Join(written, ",")
}

func NewHandler(allowed []Destination) http.Handler {
	allowed = slices.Clone(allowed)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != Path {
			http.NotFound(w, r)
			return
		}
		target, err := netip.ParseAddrPort(r.URL.Query().Get(TargetParam))
		if err == nil {
			target = netip.AddrPortFrom(target.Addr().Unmap(), target.Port())
		}
		if err != nil || !slices.ContainsFunc(allowed, func(d Destination) bool { return d.admits(target) }) {
			http.Error(w, "the bastion forwards to the tier's databases and caches alone", http.StatusForbidden)
			return
		}
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "the bastion speaks websocket alone", http.StatusUpgradeRequired)
			return
		}
		dialed, err := dial(r.Context(), target.String())
		if err != nil {
			http.Error(w, "the bastion could not reach the target: "+err.Error(), http.StatusBadGateway)
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
