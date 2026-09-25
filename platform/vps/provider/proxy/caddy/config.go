package caddy

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

//go:embed baseline.json
var baseline []byte

const (
	serverName       = "ocel"
	relayName        = "admit"
	relayListen      = "127.0.0.1:2020"
	forwardHandler   = "reverse_proxy"
	answerHandler    = "static_response"
	errorStatus      = "{http.error.status_code}"
	socketMode       = "0600"
	internalIssuer   = "internal"
	permissionModule = "http"
	internalDepth    = 8
)

var internalZones = []string{"localhost", "local", "internal", "home.arpa"}

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
	TLS  tlsApp          `json:"tls"`
	PKI  json.RawMessage `json:"pki,omitempty"`
}

type tlsApp struct {
	Certificates *certificates `json:"certificates,omitempty"`
	Automation   automation    `json:"automation"`
}

type certificates struct {
	LoadFiles []loadFile `json:"load_files"`
}

type loadFile struct {
	Certificate string `json:"certificate"`
	Key         string `json:"key"`
}

type automation struct {
	Policies []policy `json:"policies"`
	OnDemand onDemand `json:"on_demand"`
}

type policy struct {
	Subjects []string `json:"subjects,omitempty"`
	Issuers  []issuer `json:"issuers,omitempty"`
	OnDemand bool     `json:"on_demand"`
}

type issuer struct {
	Module string `json:"module"`
}

type onDemand struct {
	Permission permission `json:"permission"`
}

type permission struct {
	Module   string `json:"module"`
	Endpoint string `json:"endpoint"`
}

type httpApp struct {
	GracePeriod string            `json:"grace_period"`
	Servers     map[string]server `json:"servers"`
}

type server struct {
	Listen   []string           `json:"listen"`
	Logs     json.RawMessage    `json:"logs,omitempty"`
	Policies []connectionPolicy `json:"tls_connection_policies,omitempty"`
	Routes   []route            `json:"routes"`
	Errors   *failing           `json:"errors,omitempty"`
}

type connectionPolicy struct{}

type route struct {
	Handle []forward `json:"handle"`
}

type forward struct {
	Handler     string `json:"handler"`
	Upstreams   []dial `json:"upstreams"`
	StreamDelay string `json:"stream_close_delay,omitempty"`
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

func PermissionEndpoint(path string) string { return "http://" + relayListen + path }

func render(admission proxy.Admission) ([]byte, error) {
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
	if strings.TrimSpace(admission.Permission.Dial) == "" || !strings.HasPrefix(admission.Permission.Path, "/") {
		return nil, errors.New("an admission names no endpoint to ask whether a hostname may be issued a certificate")
	}
	pinned, err := loaded(admission.Pins)
	if err != nil {
		return nil, err
	}
	front.Policies = []connectionPolicy{{}}
	front.Routes = []route{{Handle: []forward{{
		Handler:     forwardHandler,
		Upstreams:   []dial{{Dial: admission.Upstream}},
		StreamDelay: Grace.String(),
	}}}}
	front.Errors = &failing{Routes: []failure{{Handle: []answer{{
		Handler: answerHandler,
		Status:  errorStatus,
		Headers: map[string][]string{http.CanonicalHeaderKey(edge.HeaderEdge): {admission.Edge}},
	}}}}}
	return json.Marshal(config{
		Admin:   admin{Listen: Listen()},
		Logging: seeded.Logging,
		Apps: apps{
			HTTP: httpApp{GracePeriod: Grace.String(), Servers: map[string]server{serverName: front, relayName: relayTo(admission.Permission.Dial)}},
			TLS:  tlsApp{Certificates: pinned, Automation: onDemandThrough(admission.Permission.Path)},
			PKI:  seeded.Apps.PKI,
		},
	})
}

func relayTo(socket string) server {
	return server{
		Listen: []string{relayListen},
		Routes: []route{{Handle: []forward{{Handler: forwardHandler, Upstreams: []dial{{Dial: socket}}}}}},
	}
}

func onDemandThrough(path string) automation {
	return automation{
		Policies: []policy{
			{Subjects: internalSubjects(), Issuers: []issuer{{Module: internalIssuer}}, OnDemand: true},
			{OnDemand: true},
		},
		OnDemand: onDemand{Permission: permission{Module: permissionModule, Endpoint: PermissionEndpoint(path)}},
	}
}

func internalSubjects() []string {
	subjects := []string{internalZones[0]}
	for _, zone := range internalZones {
		for depth := strings.Count(zone, ".") + 2; depth <= internalDepth; depth++ {
			subjects = append(subjects, strings.Repeat("*.", depth-strings.Count(zone, ".")-1)+zone)
		}
	}
	return subjects
}

func loaded(pins []string) (*certificates, error) {
	var files []loadFile
	for _, pin := range slices.Compact(slices.Sorted(slices.Values(pins))) {
		mounted, err := pinMount(pin)
		if err != nil {
			return nil, err
		}
		files = append(files, loadFile{Certificate: PinCertificate(mounted), Key: PinKey(mounted)})
	}
	if len(files) == 0 {
		return nil, nil
	}
	return &certificates{LoadFiles: files}, nil
}

func pinMount(path string) (string, error) {
	leaf, pinned := Pinned(path)
	if !pinned {
		return "", fmt.Errorf("the certificate pinned at %q is not directly under %s", path, PinsDir)
	}
	return PinsMount + "/" + leaf, nil
}

func unrendered(rendered []byte, admission proxy.Admission) string {
	var read struct {
		Admin struct {
			Config struct {
				Load json.RawMessage `json:"load"`
			} `json:"config"`
		} `json:"admin"`
		Apps struct {
			HTTP struct {
				Servers map[string]json.RawMessage `json:"servers"`
			} `json:"http"`
			TLS struct {
				Automation json.RawMessage `json:"automation"`
			} `json:"tls"`
		} `json:"apps"`
	}
	if json.Unmarshal(rendered, &read) != nil {
		return ""
	}
	if len(read.Admin.Config.Load) > 0 {
		return "a config loader, so the proxy serves whatever that loader fetches rather than the routing table"
	}
	if len(read.Apps.TLS.Automation) == 0 {
		return ""
	}
	if !sameJSON(read.Apps.TLS.Automation, onDemandThrough(admission.Permission.Path)) {
		return "a tls automation policy other than ordering on demand what " + admission.Permission.Dial + " admits, so the proxy may order certificates for names nothing claims"
	}
	if !sameJSON(read.Apps.HTTP.Servers[relayName], relayTo(admission.Permission.Dial)) {
		return fmt.Sprintf("a relay server %q other than one forwarding %s to %s alone, so the proxy may order certificates on the word of something other than the switchboard", relayName, relayListen, admission.Permission.Dial)
	}
	return ""
}

func sameJSON(held json.RawMessage, want any) bool {
	var read, wanted any
	written, err := json.Marshal(want)
	if err != nil || json.Unmarshal(held, &read) != nil || json.Unmarshal(written, &wanted) != nil {
		return false
	}
	return reflect.DeepEqual(read, wanted)
}
