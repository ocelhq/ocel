// PROTOTYPE — what the CLI parses and what reaches the origin (ocelhq/ocel#399).
// Sketch only; not compiled into any module.
package configsurface

import (
	"encoding/json"
	"fmt"
	"strings"
)

// rawConfig gains three fields beside provider/apps/links/domains.
type rawConfig struct {
	Edge          json.RawMessage `json:"edge"`
	DNS           *rawDNS         `json:"dns"`
	AllowDegraded []string        `json:"allowDegraded"`
}

type rawEdge struct {
	Kind    string          `json:"kind"`
	Options json.RawMessage `json:"options"`
}

type rawDNS struct {
	Kind string `json:"kind"`
	Zone string `json:"zone"`
}

// Config carries the resolved mode. The three words are the contract's Kind
// constants; the CLI never maps them to a package.
type Config struct {
	EdgeKind      string // "cloudflare" | "native" | "none"
	EdgeOptions   json.RawMessage
	DNS           *rawDNS
	AllowDegraded []string
}

var needs = []string{"edge-middleware", "edge-runtime", "ppr-resume", "edge-cache", "streaming"}

func normalize(raw rawConfig, path string) (Config, error) {
	c := Config{EdgeKind: "native", DNS: raw.DNS, AllowDegraded: raw.AllowDegraded}
	switch {
	case len(raw.Edge) == 0, string(raw.Edge) == "null":
	case string(raw.Edge) == "false":
		c.EdgeKind = "none"
	default:
		var e rawEdge
		if err := json.Unmarshal(raw.Edge, &e); err != nil || e.Kind == "" {
			return c, fmt.Errorf("%s has an invalid \"edge\": use `edge: cfEdge()` (from ocel/edge), `edge: false`, or omit it for the origin's own edge", path)
		}
		c.EdgeKind, c.EdgeOptions = e.Kind, e.Options
	}
	if c.EdgeKind == "cloudflare" && raw.DNS != nil && raw.DNS.Kind == "route53" {
		return c, fmt.Errorf("%s pairs `edge: cfEdge()` with `dns: route53()`: a Worker route needs a proxied record in a Cloudflare zone — use `dns: cloudflareDns()` or drop `dns`", path)
	}
	for _, n := range raw.AllowDegraded {
		if !known(n) {
			return c, fmt.Errorf("%s lists unknown need %q in `allowDegraded` (known: %s)", path, n, strings.Join(needs, ", "))
		}
	}
	return c, nil
}

func known(need string) bool {
	for _, n := range needs {
		if n == need {
			return true
		}
	}
	return false
}

// On the wire, beside the existing opaque provider `bytes options`:
//
//	message DeployRequest / BootstrapRequest {
//	  string edge_kind = N;        // "cloudflare" | "native" | "none"; the CLI always fills it, empty is a protocol error
//	  bytes  edge_options = N+1;   // the marker's options, opaque to the CLI like provider options
//	  Dns    dns = N+2;            // { string kind; string zone } — absent when the config has none
//	  repeated string allow_degraded = N+3;
//	}
//
// The origin's registry is the only place a kind is judged for support:
//
//	e, err := origin.EdgeFor(edge.Kind(req.EdgeKind), deps)
//	// aws does not support edge "fastly" (supported: [cloudflare native none])
//
// which the CLI renders as a typed error naming the origin and the supported
// kinds. The CLI itself refuses only shape (unknown marker, unknown need,
// the route53+cfEdge pair) — never a kind, so a new edge on the origin needs
// no CLI release.
//
// Liveness marker (#397 left it to this ticket): every front sets
// `x-ocel-edge: <kind>` on every response, including the bootstrap
// placeholder — Worker response header, CloudFront response-headers policy,
// API Gateway MOCK/integration response parameter. The probe checks the
// header, not the status. The deployment id stays the origin's header.
