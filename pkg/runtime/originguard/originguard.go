package originguard

import (
	"cmp"
	"crypto/sha256"
	"crypto/subtle"
	"io"
	"maps"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/runtime/mountedgate"
)

const OriginSecretHeader = edge.OriginSecretHeader

const (
	OriginSecretVar         = edge.OriginSecretVar
	OriginSecretPreviousVar = edge.OriginSecretPreviousVar
)

const HealthPathVar = "OCEL_HEALTH_PATH"

const (
	ProbeTokenVar    = "OCEL_PROBE_TOKEN"
	ProbeTokenHeader = "X-Ocel-Probe-Token"
)

const AppUnansweredHeader = "X-Ocel-App-Unanswered"

type Guard struct {
	expected [][sha256.Size]byte
}

func NewGuard(secrets ...string) *Guard {
	guard := &Guard{}
	for _, secret := range secrets {
		if secret != "" {
			guard.expected = append(guard.expected, sha256.Sum256([]byte(secret)))
		}
	}
	return guard
}

func GuardFromEnv(env []string) (*Guard, []string) {
	kept := make([]string, 0, len(env))
	var (
		guard    *Guard
		current  string
		previous string
	)
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		switch name {
		case OriginSecretVar:
			current = value
			guard = &Guard{}
		case OriginSecretPreviousVar:
			previous = value
		default:
			kept = append(kept, entry)
		}
	}
	if guard == nil {
		return nil, kept
	}
	return NewGuard(current, previous), kept
}

func (g *Guard) Admits(r *http.Request) bool {
	if g == nil {
		return true
	}
	presented := sha256.Sum256([]byte(r.Header.Get(OriginSecretHeader)))
	matched := 0
	for _, expected := range g.expected {
		matched |= subtle.ConstantTimeCompare(presented[:], expected[:])
	}
	return matched == 1
}

type Options struct {
	Upstream   *url.URL
	Guard      *Guard
	HealthPath string
	Ready      func() bool

	PreviewGate *mountedgate.Source
	ProbeToken  string
	Now         func() time.Time
}

func Handler(opts Options) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.Header["X-Forwarded-For"] = r.In.Header["X-Forwarded-For"]
			r.SetXForwarded()
			if scheme := r.In.Header.Get("X-Forwarded-Proto"); scheme != "" {
				r.Out.Header.Set("X-Forwarded-Proto", scheme)
			}
			r.SetURL(opts.Upstream)
			r.Out.URL.RawQuery = r.In.URL.RawQuery
			r.Out.Host = r.In.Host
			r.Out.Header.Del(OriginSecretHeader)
			r.Out.Header.Del(ProbeTokenHeader)
			if strings.EqualFold(strings.TrimSpace(r.In.Header.Get("Upgrade")), "h2c") {
				r.Out.Header.Del("Upgrade")
				r.Out.Header.Del("Connection")
				r.Out.Header.Del("HTTP2-Settings")
			}
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			w.Header().Set(AppUnansweredHeader, "true")
			http.Error(w, "the app did not answer", http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if opts.Ready != nil && !opts.Ready() {
			w.Header().Set(AppUnansweredHeader, "true")
			http.Error(w, "the app is still starting", http.StatusServiceUnavailable)
			return
		}
		if !probe(r, opts.HealthPath) && !opts.Guard.Admits(r) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if opts.PreviewGate != nil && !admitPreview(w, r, opts, proxy) {
			return
		}
		proxy.ServeHTTP(w, r)
	})
}

func admitPreview(w http.ResponseWriter, r *http.Request, opts Options, proxy http.Handler) bool {
	gate, err := opts.PreviewGate.Current()
	if err != nil {
		answerUnavailable(w, r, opts, proxy)
		return false
	}
	if gate == nil {
		return true
	}
	verdict := gate.Check(previewgate.Request{
		Method: r.Method,
		Host:   r.Host,
		Target: r.URL.RequestURI(),
		Header: previewgate.Header(r.Header),
	}, now(opts))
	switch {
	case verdict.Response == nil:
		r.Header = http.Header(verdict.Forward)
		maps.Copy(w.Header(), http.Header(verdict.ResponseHeader))
		return true
	case isTokenedProbe(r, opts):
		answerStatusAlone(w, r, proxy)
	default:
		maps.Copy(w.Header(), http.Header(verdict.Response.Header))
		w.WriteHeader(verdict.Response.Status)
		_, _ = io.WriteString(w, verdict.Response.Body)
	}
	return false
}

func now(opts Options) time.Time {
	if opts.Now != nil {
		return opts.Now()
	}
	return time.Now()
}

func answerUnavailable(w http.ResponseWriter, r *http.Request, opts Options, proxy http.Handler) {
	if isTokenedProbe(r, opts) {
		answerStatusAlone(w, r, proxy)
		return
	}
	w.Header().Set("Cache-Control", previewgate.CacheControl)
	w.Header().Set("X-Robots-Tag", previewgate.RobotsTag)
	http.Error(w, "Service temporarily unavailable.", http.StatusServiceUnavailable)
}

type statusAlone struct {
	header http.Header
	status int
}

func (s *statusAlone) Header() http.Header { return s.header }

func (s *statusAlone) Write(body []byte) (int, error) { return len(body), nil }

func (s *statusAlone) WriteHeader(status int) {
	if s.status == 0 {
		s.status = status
	}
}

func answerStatusAlone(w http.ResponseWriter, r *http.Request, proxy http.Handler) {
	answered := &statusAlone{header: http.Header{}}
	proxy.ServeHTTP(answered, r)
	w.Header().Set("Cache-Control", previewgate.CacheControl)
	w.Header().Set("X-Robots-Tag", previewgate.RobotsTag)
	w.WriteHeader(cmp.Or(answered.status, http.StatusOK))
}

func probe(r *http.Request, healthPath string) bool {
	return healthPath != "" && r.URL.Path == healthPath && slices.Contains([]string{http.MethodGet, http.MethodHead}, r.Method)
}

func isTokenedProbe(r *http.Request, opts Options) bool {
	if opts.ProbeToken == "" || !probe(r, opts.HealthPath) {
		return false
	}
	presented := sha256.Sum256([]byte(r.Header.Get(ProbeTokenHeader)))
	expected := sha256.Sum256([]byte(opts.ProbeToken))
	return subtle.ConstantTimeCompare(presented[:], expected[:]) == 1
}
