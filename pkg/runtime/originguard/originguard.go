package originguard

import (
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
	"github.com/ocelhq/ocel/pkg/runtime/previewlogin"
)

const OriginSecretHeader = edge.OriginSecretHeader

const (
	OriginSecretVar         = edge.OriginSecretVar
	OriginSecretPreviousVar = edge.OriginSecretPreviousVar
)

const HealthPathVar = "OCEL_HEALTH_PATH"

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
	Upstream     *url.URL
	Guard        *Guard
	HealthPath   string
	Ready        func() bool
	PreviewLogin *previewlogin.Login
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
		if opts.PreviewLogin != nil && previewlogin.IsLogin(r.Method, r.URL.Path) {
			answerPreviewLogin(w, r, opts)
			return
		}
		if opts.Ready != nil && !opts.Ready() {
			w.Header().Set(AppUnansweredHeader, "true")
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

func answerPreviewLogin(w http.ResponseWriter, r *http.Request, opts Options) {
	if !opts.Guard.Admits(r) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body, err := previewlogin.ReadBody(r.Body)
	if err != nil {
		http.Error(w, "the login body could not be read", http.StatusBadRequest)
		return
	}
	answer := opts.PreviewLogin.Answer(previewgate.Request{
		Method: r.Method,
		Host:   r.Host,
		Target: r.URL.RequestURI(),
		Header: previewgate.Header(r.Header),
		Body:   body,
	}, time.Now())
	maps.Copy(w.Header(), http.Header(answer.Header))
	w.WriteHeader(answer.Status)
	_, _ = io.WriteString(w, answer.Body)
}

func probe(r *http.Request, healthPath string) bool {
	return healthPath != "" && r.URL.Path == healthPath && slices.Contains([]string{http.MethodGet, http.MethodHead}, r.Method)
}
