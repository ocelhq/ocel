package providerkit

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	livenessTimeout  = 15 * time.Second
	nameserverWindow = 5 * time.Second
)

type Names interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
	LookupNS(ctx context.Context, name string) ([]*net.NS, error)
	LookupCNAME(ctx context.Context, host string) (string, error)
}

type Unanswered struct{ Cause string }

func (u Unanswered) Error() string { return u.Cause }

type Liveness struct {
	System    Names
	Authority func(nameserver string) Names
	Dial      func(ctx context.Context, network, address string) (net.Conn, error)
	TLS       *tls.Config
	Front     *url.URL
	Loopback  func(ctx context.Context, hostname string) (edge.Kind, error)

	LoopbackOnly bool

	mu        sync.Mutex
	unreached map[string]string
}

var (
	_ Prober    = (*Liveness)(nil)
	_ Diagnoser = (*Liveness)(nil)
)

func (l *Liveness) Serving(ctx context.Context, _ edge.Kind, hostname string) (edge.Kind, error) {
	answered, err := l.ask(ctx, edge.ProbeHostname(hostname))
	var unanswered Unanswered
	switch {
	case err == nil:
		l.record(hostname, "")
		return answered, nil
	case ctx.Err() != nil:
		return "", ctx.Err()
	case errors.As(err, &unanswered):
		l.record(hostname, unanswered.Cause)
		return "", nil
	default:
		return "", err
	}
}

func (l *Liveness) Unreached(hostname string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.unreached[hostname]
}

func (l *Liveness) record(hostname, cause string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if cause == "" {
		delete(l.unreached, hostname)
		return
	}
	if l.unreached == nil {
		l.unreached = map[string]string{}
	}
	l.unreached[hostname] = cause
}

func (l *Liveness) ask(ctx context.Context, hostname string) (edge.Kind, error) {
	if l.Loopback != nil && (l.LoopbackOnly || edge.Loopback(hostname)) {
		return l.Loopback(ctx, hostname)
	}
	scheme, addresses, err := l.locate(ctx, hostname)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", Unanswered{Cause: err.Error()}
	}
	return l.request(ctx, scheme, hostname, addresses)
}

func (l *Liveness) request(ctx context.Context, scheme, hostname string, addresses []string) (edge.Kind, error) {
	var config *tls.Config
	if l.TLS != nil {
		config = l.TLS.Clone()
	}
	client := &http.Client{
		Timeout: livenessTimeout,
		Transport: &http.Transport{
			TLSClientConfig:   config,
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				var failed error
				for _, address := range addresses {
					held, err := l.dial(ctx, network, address)
					if err == nil {
						return held, nil
					}
					failed = err
				}
				return nil, failed
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	asked, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+hostname+edge.LivenessProbePath, nil)
	if err != nil {
		return "", err
	}
	said, err := client.Do(asked)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var reached *url.Error
		if errors.As(err, &reached) {
			err = reached.Err
		}
		return "", Unanswered{Cause: err.Error()}
	}
	defer said.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(said.Body, 1<<12))
	return edge.Kind(strings.TrimSpace(said.Header.Get(edge.HeaderEdge))), nil
}

func (l *Liveness) dial(ctx context.Context, network, address string) (net.Conn, error) {
	if l.Dial != nil {
		return l.Dial(ctx, network, address)
	}
	return (&net.Dialer{}).DialContext(ctx, network, address)
}

func (l *Liveness) system() Names {
	if l.System != nil {
		return l.System
	}
	return net.DefaultResolver
}

func (l *Liveness) authority(nameserver string) Names {
	if l.Authority != nil {
		return l.Authority(nameserver)
	}
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return l.dial(ctx, network, net.JoinHostPort(nameserver, "53"))
	}}
}

func (l *Liveness) locate(ctx context.Context, hostname string) (string, []string, error) {
	if l.Front != nil {
		return l.Front.Scheme, []string{frontAddress(l.Front)}, nil
	}
	resolved, err := l.system().LookupHost(ctx, hostname)
	if err == nil && len(resolved) > 0 {
		return "https", onPort(resolved, "443"), nil
	}
	if ctx.Err() != nil {
		return "", nil, ctx.Err()
	}
	if err == nil {
		err = errors.New("no address")
	}
	hinted, hintErr := l.hint(ctx, hostname)
	if hintErr != nil {
		return "", nil, fmt.Errorf("%s resolves to nothing here (%w), and %w", hostname, err, hintErr)
	}
	return "https", hinted, nil
}

func frontAddress(front *url.URL) string {
	if front.Port() != "" {
		return front.Host
	}
	if front.Scheme == "http" {
		return net.JoinHostPort(front.Hostname(), "80")
	}
	return net.JoinHostPort(front.Hostname(), "443")
}

func onPort(addresses []string, port string) []string {
	held := make([]string, 0, len(addresses))
	for _, address := range addresses {
		held = append(held, net.JoinHostPort(address, port))
	}
	return held
}

func (l *Liveness) hint(ctx context.Context, hostname string) ([]string, error) {
	zone, nameservers, err := l.zoneOf(ctx, hostname)
	if err != nil {
		return nil, err
	}
	failures := make([]string, 0, len(nameservers))
	for _, nameserver := range nameservers {
		asking, stop := context.WithTimeout(ctx, nameserverWindow)
		addresses, err := l.answer(asking, nameserver, hostname)
		stop()
		if err == nil {
			return addresses, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		failures = append(failures, fmt.Sprintf("%s: %v", nameserver, err))
	}
	return nil, fmt.Errorf("no nameserver for %s answers for it yet (%s)", zone, strings.Join(failures, "; "))
}

func (l *Liveness) answer(ctx context.Context, nameserver, hostname string) ([]string, error) {
	asked := l.authority(nameserver)
	canonical, err := asked.LookupCNAME(ctx, hostname+".")
	if err != nil {
		return nil, err
	}
	if canonical = strings.TrimSuffix(canonical, "."); !strings.EqualFold(canonical, hostname) {
		addresses, err := l.system().LookupHost(ctx, canonical)
		if err != nil {
			return nil, fmt.Errorf("%s is an alias for %s, which resolves to nothing yet: %w", hostname, canonical, err)
		}
		return onPort(addresses, "443"), nil
	}
	addresses, err := asked.LookupHost(ctx, hostname+".")
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("it holds no address for %s", hostname)
	}
	return onPort(addresses, "443"), nil
}

func (l *Liveness) zoneOf(ctx context.Context, hostname string) (string, []string, error) {
	labels := strings.Split(strings.TrimSuffix(hostname, "."), ".")
	for at := range max(len(labels)-1, 1) {
		candidate := strings.Join(labels[at:], ".")
		held, err := l.system().LookupNS(ctx, candidate+".")
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		if err != nil || len(held) == 0 {
			continue
		}
		named := make([]string, 0, len(held))
		for _, ns := range held {
			named = append(named, strings.TrimSuffix(ns.Host, "."))
		}
		return candidate, named, nil
	}
	return "", nil, fmt.Errorf("no zone at or above %s names a nameserver", hostname)
}
