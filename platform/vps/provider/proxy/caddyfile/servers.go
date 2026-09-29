package caddyfile

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

const (
	adminServers = "http://127.0.0.1:2019/config/apps/http/servers"
	curl         = "curl"
)

type server struct {
	Routes []route `json:"routes"`
}

type route struct {
	Match  []matcher `json:"match"`
	Handle []handler `json:"handle"`
}

type matcher struct {
	Host []string `json:"host"`
}

type handler struct {
	Handler    string              `json:"handler"`
	Routes     []route             `json:"routes"`
	Upstreams  []upstream          `json:"upstreams"`
	StatusCode json.RawMessage     `json:"status_code"`
	Headers    map[string][]string `json:"headers"`
}

const (
	redirectHandler  = "static_response"
	redirectStatus   = "308"
	redirectLocation = "https://{http.request.host}{http.request.uri}"
)

type upstream struct {
	Dial string `json:"dial"`
}

type site struct {
	server             string
	route              int
	hosts              []string
	reachesSwitchboard bool
	redirectsToHTTPS   bool
}

func (s site) String() string {
	return fmt.Sprintf("server %s, route %d (%s)", s.server, s.route, strings.Join(s.hosts, ", "))
}

func (s site) isPlacedBlock(placed proxy.Spec) bool {
	open, shielded := split(placed)
	plain := make([]string, 0, len(shielded))
	for _, each := range shielded {
		plain = append(plain, each.hostname)
	}
	hosts := hostSet(s.hosts)
	if len(hosts) == 0 {
		return false
	}
	if s.redirectsToHTTPS {
		return slices.Equal(hosts, hostSet(plain))
	}
	if !s.reachesSwitchboard {
		return false
	}
	return slices.Equal(hosts, hostSet(open)) || len(hosts) == 1 && slices.Contains(hostSet(plain), hosts[0])
}

func hostSet(hosts []string) []string {
	set := make([]string, 0, len(hosts))
	for _, host := range hosts {
		set = append(set, strings.ToLower(host))
	}
	slices.Sort(set)
	return slices.Compact(set)
}

func (c Caddyfile) adminReading() []string {
	if c.Container == "" {
		return []string{curl, "-fsS", adminServers}
	}
	return []string{"docker", "exec", c.Container, "wget", "-qO-", adminServers}
}

func (c Caddyfile) sites(ctx context.Context) ([]site, error) {
	said, err := c.Box.Ran(ctx, "read what "+c.named()+" serves from its admin endpoint", c.adminReading())
	if err != nil {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"%s did not answer on its admin endpoint %s: %v\n"+
				"Ocel reads what your Caddy serves and reloads it through that endpoint, so a Caddy run with `admin off` or with admin moved elsewhere cannot front ocel; restore the default `admin localhost:2019`",
			c.named(), adminServers, err)
	}
	var servers map[string]server
	if err := json.Unmarshal([]byte(said), &servers); err != nil {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"%s answered %s with %q, which is no list of servers: %v", c.named(), adminServers, said, err)
	}
	var found []site
	for _, name := range slices.Sorted(maps.Keys(servers)) {
		for at, top := range servers[name].Routes {
			found = append(found, site{server: name, route: at + 1, hosts: top.hosts(),
				reachesSwitchboard: top.reaches(c.upstream()), redirectsToHTTPS: top.redirects()})
		}
	}
	return found, nil
}

func (r route) hosts() []string {
	var named []string
	for _, match := range r.Match {
		named = append(named, match.Host...)
	}
	for _, handle := range r.Handle {
		for _, nested := range handle.Routes {
			named = append(named, nested.hosts()...)
		}
	}
	return named
}

func (r route) reaches(dial string) bool {
	for _, handle := range r.Handle {
		if handle.Handler == "reverse_proxy" && slices.ContainsFunc(handle.Upstreams, func(up upstream) bool { return up.Dial == dial }) {
			return true
		}
		if slices.ContainsFunc(handle.Routes, func(nested route) bool { return nested.reaches(dial) }) {
			return true
		}
	}
	return false
}

func (r route) redirects() bool {
	var answers []handler
	for _, handle := range r.Handle {
		if handle.Handler == redirectHandler {
			answers = append(answers, handle)
		}
		for _, nested := range handle.Routes {
			answers = append(answers, nested.Handle...)
		}
	}
	return len(answers) == 1 && answers[0].Handler == redirectHandler && string(answers[0].StatusCode) == redirectStatus &&
		slices.Equal(answers[0].Headers["Location"], []string{redirectLocation})
}

func covers(pattern, hostname string) bool {
	want, got := strings.Split(strings.ToLower(pattern), "."), strings.Split(strings.ToLower(hostname), ".")
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		if want[i] != "*" && want[i] != got[i] {
			return false
		}
	}
	return true
}

func (s site) covering(hostname string) (string, bool) {
	for _, host := range s.hosts {
		if covers(host, hostname) {
			return host, true
		}
	}
	return "", false
}

func collision(sites []site, placed proxy.Spec, hostname string) (site, string, bool) {
	for _, each := range sites {
		if each.isPlacedBlock(placed) {
			continue
		}
		if host, covered := each.covering(hostname); covered {
			return each, host, true
		}
	}
	return site{}, "", false
}
