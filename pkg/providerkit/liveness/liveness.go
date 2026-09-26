package liveness

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
	probeTimeout     = 15 * time.Second
	nameserverWindow = 5 * time.Second
)

type DNSLookup interface {
	Host(ctx context.Context, host string) ([]string, error)
	NS(ctx context.Context, name string) ([]*net.NS, error)
	CNAME(ctx context.Context, host string) (string, error)
}

type netDNS struct{ resolver *net.Resolver }

func (d netDNS) Host(ctx context.Context, host string) ([]string, error) {
	return d.resolver.LookupHost(ctx, host)
}

func (d netDNS) NS(ctx context.Context, name string) ([]*net.NS, error) {
	return d.resolver.LookupNS(ctx, name)
}

func (d netDNS) CNAME(ctx context.Context, host string) (string, error) {
	return d.resolver.LookupCNAME(ctx, host)
}

type ProbeUnanswered struct{ Cause string }

func (u ProbeUnanswered) Error() string { return u.Cause }

type Net struct {
	System       DNSLookup
	Authority    func(nameserver string) DNSLookup
	Dial         func(ctx context.Context, network, address string) (net.Conn, error)
	TLS          *tls.Config
	ProbeAddress *url.URL
	Loopback     func(ctx context.Context, hostname string) (edge.Kind, error)

	LoopbackOnly bool

	mu           sync.Mutex
	lastFailures map[string]string
}

func (l *Net) ServingEdge(ctx context.Context, _ edge.Kind, hostname string) (edge.Kind, error) {
	answered, err := l.probe(ctx, edge.ProbeHostname(hostname))
	var unanswered ProbeUnanswered
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

func (l *Net) LastProbeFailure(hostname string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastFailures[hostname]
}

func (l *Net) record(hostname, cause string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if cause == "" {
		delete(l.lastFailures, hostname)
		return
	}
	if l.lastFailures == nil {
		l.lastFailures = map[string]string{}
	}
	l.lastFailures[hostname] = cause
}

func (l *Net) probe(ctx context.Context, hostname string) (edge.Kind, error) {
	if l.Loopback != nil && (l.LoopbackOnly || edge.Loopback(hostname)) {
		return l.Loopback(ctx, hostname)
	}
	scheme, addresses, err := l.resolveTarget(ctx, hostname)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", ProbeUnanswered{Cause: err.Error()}
	}
	return l.request(ctx, scheme, hostname, addresses)
}

func (l *Net) request(ctx context.Context, scheme, hostname string, addresses []string) (edge.Kind, error) {
	var config *tls.Config
	if l.TLS != nil {
		config = l.TLS.Clone()
	}
	client := &http.Client{
		Timeout: probeTimeout,
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
		return "", ProbeUnanswered{Cause: err.Error()}
	}
	defer said.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(said.Body, 1<<12))
	return edge.Kind(strings.TrimSpace(said.Header.Get(edge.HeaderEdge))), nil
}

func (l *Net) dial(ctx context.Context, network, address string) (net.Conn, error) {
	if l.Dial != nil {
		return l.Dial(ctx, network, address)
	}
	return (&net.Dialer{}).DialContext(ctx, network, address)
}

func (l *Net) system() DNSLookup {
	if l.System != nil {
		return l.System
	}
	return netDNS{resolver: net.DefaultResolver}
}

func (l *Net) authority(nameserver string) DNSLookup {
	if l.Authority != nil {
		return l.Authority(nameserver)
	}
	return netDNS{resolver: &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return l.dial(ctx, network, net.JoinHostPort(nameserver, "53"))
	}}}
}

func (l *Net) resolveTarget(ctx context.Context, hostname string) (string, []string, error) {
	if l.ProbeAddress != nil {
		return l.ProbeAddress.Scheme, []string{dialAddress(l.ProbeAddress)}, nil
	}
	resolved, err := l.system().Host(ctx, hostname)
	if err == nil && len(resolved) > 0 {
		return "https", onPort(resolved, "443"), nil
	}
	if ctx.Err() != nil {
		return "", nil, ctx.Err()
	}
	if err == nil {
		err = errors.New("no address")
	}
	authoritative, authoritativeErr := l.resolveAuthoritative(ctx, hostname)
	if authoritativeErr != nil {
		return "", nil, fmt.Errorf("%s resolves to nothing here (%w), and %w", hostname, err, authoritativeErr)
	}
	return "https", authoritative, nil
}

func dialAddress(target *url.URL) string {
	if target.Port() != "" {
		return target.Host
	}
	if target.Scheme == "http" {
		return net.JoinHostPort(target.Hostname(), "80")
	}
	return net.JoinHostPort(target.Hostname(), "443")
}

func onPort(addresses []string, port string) []string {
	held := make([]string, 0, len(addresses))
	for _, address := range addresses {
		held = append(held, net.JoinHostPort(address, port))
	}
	return held
}

func (l *Net) resolveAuthoritative(ctx context.Context, hostname string) ([]string, error) {
	zone, nameservers, err := l.zoneOf(ctx, hostname)
	if err != nil {
		return nil, err
	}
	failures := make([]string, 0, len(nameservers))
	for _, nameserver := range nameservers {
		asking, stop := context.WithTimeout(ctx, nameserverWindow)
		addresses, err := l.queryNameserver(asking, nameserver, hostname)
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

func (l *Net) queryNameserver(ctx context.Context, nameserver, hostname string) ([]string, error) {
	asked := l.authority(nameserver)
	canonical, err := asked.CNAME(ctx, hostname+".")
	if err != nil {
		return nil, err
	}
	if canonical = strings.TrimSuffix(canonical, "."); !strings.EqualFold(canonical, hostname) {
		addresses, err := l.system().Host(ctx, canonical)
		if err != nil {
			return nil, fmt.Errorf("%s is an alias for %s, which resolves to nothing yet: %w", hostname, canonical, err)
		}
		return onPort(addresses, "443"), nil
	}
	addresses, err := asked.Host(ctx, hostname+".")
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("it holds no address for %s", hostname)
	}
	return onPort(addresses, "443"), nil
}

func (l *Net) zoneOf(ctx context.Context, hostname string) (string, []string, error) {
	labels := strings.Split(strings.TrimSuffix(hostname, "."), ".")
	for at := range max(len(labels)-1, 1) {
		candidate := strings.Join(labels[at:], ".")
		held, err := l.system().NS(ctx, candidate+".")
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
