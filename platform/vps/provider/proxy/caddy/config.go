package caddy

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

//go:embed baseline.json
var baseline []byte

const (
	serverName     = "ocel"
	forwardHandler = "reverse_proxy"
	answerHandler  = "static_response"
	errorStatus    = "{http.error.status_code}"
	socketMode     = "0600"
)

type config struct {
	Admin   admin           `json:"admin"`
	Logging json.RawMessage `json:"logging,omitempty"`
	Apps    apps            `json:"apps"`
}

type admin struct {
	Listen string `json:"listen"`
}

type apps struct {
	HTTP httpApp         `json:"http"`
	TLS  *tlsApp         `json:"tls,omitempty"`
	PKI  json.RawMessage `json:"pki,omitempty"`
}

type tlsApp struct {
	Certificates certificates `json:"certificates"`
}

type certificates struct {
	LoadFiles []loadFile `json:"load_files"`
}

type loadFile struct {
	Certificate string   `json:"certificate"`
	Key         string   `json:"key"`
	Tags        []string `json:"tags"`
}

type httpApp struct {
	GracePeriod string            `json:"grace_period"`
	Servers     map[string]server `json:"servers"`
}

type server struct {
	Listen    []string        `json:"listen"`
	Logs      json.RawMessage `json:"logs,omitempty"`
	Automatic *automatic      `json:"automatic_https,omitempty"`
	Routes    []route         `json:"routes"`
	Errors    *failing        `json:"errors,omitempty"`
}

type automatic struct {
	SkipCertificates []string `json:"skip_certificates"`
}

type route struct {
	Match  []match   `json:"match,omitempty"`
	Handle []forward `json:"handle"`
}

type match struct {
	Host []string `json:"host"`
}

type forward struct {
	Handler     string `json:"handler"`
	Upstreams   []dial `json:"upstreams"`
	StreamDelay string `json:"stream_close_delay"`
}

type dial struct {
	Dial string `json:"dial"`
}

type failing struct {
	Routes []failure `json:"routes"`
}

type failure struct {
	Handle []answer `json:"handle"`
}

type answer struct {
	Handler string              `json:"handler"`
	Status  string              `json:"status_code"`
	Headers map[string][]string `json:"headers"`
}

func Listen() string { return "unix/" + AdminSocket + "|" + socketMode }

func Render(admission proxy.Admission) ([]byte, error) {
	var seeded config
	if err := json.Unmarshal(baseline, &seeded); err != nil {
		return nil, fmt.Errorf("the baseline caddy config is not json: %w", err)
	}
	front, held := seeded.Apps.HTTP.Servers[serverName]
	if !held {
		return nil, fmt.Errorf("the baseline caddy config declares no %s server", serverName)
	}
	if strings.TrimSpace(admission.Upstream) == "" {
		return nil, errors.New("an admission names no upstream to forward to")
	}
	if strings.TrimSpace(admission.Edge) == "" {
		return nil, errors.New("an admission names no edge for the proxy's answers to carry")
	}
	hostnames, err := served(admission)
	if err != nil {
		return nil, err
	}
	pinned, err := loaded(admission.Entries)
	if err != nil {
		return nil, err
	}
	forwarding := []forward{{
		Handler:     forwardHandler,
		Upstreams:   []dial{{Dial: admission.Upstream}},
		StreamDelay: spelled(Grace),
	}}
	front.Errors = &failing{Routes: []failure{{Handle: []answer{{
		Handler: answerHandler,
		Status:  errorStatus,
		Headers: map[string][]string{http.CanonicalHeaderKey(edge.HeaderEdge): {admission.Edge}},
	}}}}}
	front.Routes = nil
	if len(hostnames) > 0 {
		front.Routes = append(front.Routes, route{Match: []match{{Host: hostnames}}, Handle: forwarding})
	}
	front.Routes = append(front.Routes, route{Handle: forwarding})
	if admission.PreviewBase != "" {
		front.Automatic = &automatic{SkipCertificates: []string{edge.PreviewWildcard(admission.PreviewBase)}}
	}
	return json.Marshal(config{
		Admin:   admin{Listen: Listen()},
		Logging: seeded.Logging,
		Apps: apps{
			HTTP: httpApp{GracePeriod: spelled(Grace), Servers: map[string]server{serverName: front}},
			TLS:  pinned,
			PKI:  seeded.Apps.PKI,
		},
	})
}

func served(admission proxy.Admission) ([]string, error) {
	var hostnames []string
	for _, entry := range admission.Entries {
		if strings.TrimSpace(entry.Hostname) == "" || strings.ContainsAny(entry.Hostname, "*/ ") {
			return nil, fmt.Errorf("an admission entry names %q, which is no hostname the front proxy can hold a certificate for", entry.Hostname)
		}
		hostnames = append(hostnames, strings.ToLower(entry.Hostname))
	}
	if admission.PreviewBase != "" {
		wildcard := edge.PreviewWildcard(admission.PreviewBase)
		hostnames = append(hostnames, edge.ProbeHostname(wildcard), wildcard)
	}
	slices.Sort(hostnames)
	return slices.Compact(hostnames), nil
}

func loaded(entries []proxy.Entry) (*tlsApp, error) {
	var files []loadFile
	at := map[string]int{}
	for _, entry := range slices.SortedFunc(slices.Values(entries), byHostname) {
		if entry.Pin == "" {
			continue
		}
		mounted, err := pinMount(entry.Pin)
		if err != nil {
			return nil, err
		}
		hostname := strings.ToLower(entry.Hostname)
		if held, loaded := at[entry.Pin]; loaded {
			if !slices.Contains(files[held].Tags, hostname) {
				files[held].Tags = append(files[held].Tags, hostname)
			}
			continue
		}
		at[entry.Pin] = len(files)
		files = append(files, loadFile{Certificate: PinCertificate(mounted), Key: PinKey(mounted), Tags: []string{hostname}})
	}
	if len(files) == 0 {
		return nil, nil
	}
	return &tlsApp{Certificates: certificates{LoadFiles: files}}, nil
}

func pinMount(path string) (string, error) {
	leaf, beneath := strings.CutPrefix(path, PinsDir+"/")
	if !beneath || leaf == "" || strings.Contains(leaf, "/") || leaf == "." || leaf == ".." {
		return "", fmt.Errorf("the certificate pinned at %q is not directly under %s", path, PinsDir)
	}
	return PinsMount + "/" + leaf, nil
}

func byHostname(a, b proxy.Entry) int { return strings.Compare(a.Hostname, b.Hostname) }

func spelled(window time.Duration) string {
	return fmt.Sprintf("%ds", int(window.Round(time.Second).Seconds()))
}

func Foreign(rendered []byte) string {
	var read struct {
		Admin struct {
			Config struct {
				Load json.RawMessage `json:"load"`
			} `json:"config"`
		} `json:"admin"`
		Apps struct {
			TLS struct {
				Automation json.RawMessage `json:"automation"`
			} `json:"tls"`
		} `json:"apps"`
	}
	switch {
	case json.Unmarshal(rendered, &read) != nil:
		return ""
	case len(read.Apps.TLS.Automation) > 0:
		return "a tls automation policy, so the proxy may order certificates for any name it is asked for"
	case len(read.Admin.Config.Load) > 0:
		return "a config loader, so the proxy serves whatever that loader fetches rather than the routing table"
	default:
		return ""
	}
}
