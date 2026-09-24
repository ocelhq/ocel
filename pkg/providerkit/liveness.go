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

const livenessTimeout = 15 * time.Second

type Liveness struct {
	Locate    func(ctx context.Context, hostname string) ([]string, error)
	TLS       *tls.Config
	Transport http.RoundTripper

	mu        sync.Mutex
	unreached map[string]string
}

var (
	_ Prober    = (*Liveness)(nil)
	_ Diagnoser = (*Liveness)(nil)
)

func (l *Liveness) Serving(ctx context.Context, _ edge.Kind, hostname string) (edge.Kind, error) {
	probed := edge.ProbeHostname(hostname)
	transport := l.Transport
	if transport == nil {
		locate := l.Locate
		if locate == nil {
			locate = systemAuthority().locate
		}
		addresses, err := locate(ctx, probed)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			l.stopped(hostname, err.Error())
			return "", nil
		}
		transport = l.located(addresses)
	}
	client := &http.Client{
		Timeout:       livenessTimeout,
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	said, err := client.Do(mustRequest(ctx, probed))
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		l.stopped(hostname, transportCause(err))
		return "", nil
	}
	defer said.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(said.Body, 1<<12))
	l.stopped(hostname, "")
	return edge.Kind(strings.TrimSpace(said.Header.Get(edge.HeaderEdge))), nil
}

func (l *Liveness) Unreached(hostname string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.unreached[hostname]
}

func (l *Liveness) stopped(hostname, cause string) {
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

func mustRequest(ctx context.Context, hostname string) *http.Request {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+hostname+"/", nil)
	return request
}

func (l *Liveness) located(addresses []string) http.RoundTripper {
	var config *tls.Config
	if l.TLS != nil {
		config = l.TLS.Clone()
	}
	return &http.Transport{
		TLSClientConfig:   config,
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var failed error
			for _, address := range addresses {
				held, err := (&net.Dialer{}).DialContext(ctx, network, address)
				if err == nil {
					return held, nil
				}
				failed = err
			}
			return nil, failed
		},
	}
}

func transportCause(err error) string {
	var reached *url.Error
	if errors.As(err, &reached) {
		err = reached.Err
	}
	return err.Error()
}

type authority struct {
	nameservers func(ctx context.Context, zone string) ([]string, error)
	answer      func(ctx context.Context, nameserver, hostname string) (string, []string, error)
	public      func(ctx context.Context, hostname string) ([]string, error)
}

func systemAuthority() authority {
	return authority{
		nameservers: func(ctx context.Context, zone string) ([]string, error) {
			held, err := net.DefaultResolver.LookupNS(ctx, zone+".")
			if err != nil {
				return nil, err
			}
			named := make([]string, 0, len(held))
			for _, ns := range held {
				named = append(named, strings.TrimSuffix(ns.Host, "."))
			}
			return named, nil
		},
		answer: func(ctx context.Context, nameserver, hostname string) (string, []string, error) {
			asked := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(nameserver, "53"))
			}}
			canonical, err := asked.LookupCNAME(ctx, hostname+".")
			if err != nil {
				return "", nil, err
			}
			if canonical = strings.TrimSuffix(canonical, "."); !strings.EqualFold(canonical, hostname) {
				return canonical, nil, nil
			}
			addresses, err := asked.LookupHost(ctx, hostname+".")
			return hostname, addresses, err
		},
		public: func(ctx context.Context, hostname string) ([]string, error) {
			return net.DefaultResolver.LookupHost(ctx, hostname+".")
		},
	}
}

func (a authority) locate(ctx context.Context, hostname string) ([]string, error) {
	zone, nameservers, err := a.zoneOf(ctx, hostname)
	if err != nil {
		return nil, err
	}
	var located []string
	for _, nameserver := range nameservers {
		canonical, addresses, err := a.answer(ctx, nameserver, hostname)
		if err != nil {
			return nil, fmt.Errorf("%s is not on %s, one of the nameservers for %s, yet: %w", hostname, nameserver, zone, err)
		}
		if located != nil {
			continue
		}
		canonical = strings.TrimSuffix(canonical, ".")
		if !strings.EqualFold(canonical, hostname) {
			if addresses, err = a.public(ctx, canonical); err != nil {
				return nil, fmt.Errorf("%s is an alias for %s, which resolves to nothing yet: %w", hostname, canonical, err)
			}
		}
		for _, address := range addresses {
			located = append(located, net.JoinHostPort(address, "443"))
		}
	}
	if len(located) == 0 {
		return nil, fmt.Errorf("the nameservers for %s hold no address for %s", zone, hostname)
	}
	return located, nil
}

func (a authority) zoneOf(ctx context.Context, hostname string) (string, []string, error) {
	labels := strings.Split(strings.TrimSuffix(hostname, "."), ".")
	var zone string
	var held []string
	for depth := 2; depth <= max(len(labels)-1, 2) && depth <= len(labels); depth++ {
		candidate := strings.Join(labels[len(labels)-depth:], ".")
		nameservers, err := a.nameservers(ctx, candidate)
		if err != nil {
			var missing *net.DNSError
			if errors.As(err, &missing) && missing.IsNotFound {
				if zone != "" {
					break
				}
				continue
			}
			return "", nil, err
		}
		if len(nameservers) > 0 {
			zone, held = candidate, nameservers
		}
	}
	if zone == "" {
		return "", nil, fmt.Errorf("no zone above %s names a nameserver", hostname)
	}
	return zone, held, nil
}
