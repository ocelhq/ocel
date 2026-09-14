package front

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const OriginSecretHeader = edge.OriginSecretHeader

const OriginSecretVar = edge.OriginSecretVar

const HealthPathVar = "OCEL_HEALTH_PATH"

type Guard struct {
	expected [sha256.Size]byte
	open     bool
}

func NewGuard(secret string) *Guard {
	if secret == "" {
		return &Guard{}
	}
	return &Guard{expected: sha256.Sum256([]byte(secret)), open: true}
}

func GuardFromEnv(env []string) (*Guard, []string) {
	kept := make([]string, 0, len(env))
	var guard *Guard
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		if name == OriginSecretVar {
			guard = NewGuard(value)
			continue
		}
		kept = append(kept, entry)
	}
	return guard, kept
}

func (g *Guard) Admits(r *http.Request) bool {
	if g == nil {
		return true
	}
	if !g.open {
		return false
	}
	presented := sha256.Sum256([]byte(r.Header.Get(OriginSecretHeader)))
	return subtle.ConstantTimeCompare(presented[:], g.expected[:]) == 1
}

type Options struct {
	Upstream   *url.URL
	Guard      *Guard
	HealthPath string
	Ready      func() bool
}

func Handler(opts Options) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetXForwarded()
			r.SetURL(opts.Upstream)
			r.Out.Host = r.In.Host
			r.Out.Header.Del(OriginSecretHeader)
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "the app did not answer", http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if opts.Ready != nil && !opts.Ready() {
			http.Error(w, "the app is still starting", http.StatusServiceUnavailable)
			return
		}
		if probe(r, opts.HealthPath) || opts.Guard.Admits(r) {
			proxy.ServeHTTP(w, r)
			return
		}
		w.WriteHeader(http.StatusForbidden)
	})
}

func probe(r *http.Request, healthPath string) bool {
	return healthPath != "" && r.URL.Path == healthPath && slices.Contains([]string{http.MethodGet, http.MethodHead}, r.Method)
}
