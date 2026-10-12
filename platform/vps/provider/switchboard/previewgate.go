package switchboard

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/edge/previewgate"
)

var cacheOverrides = []string{"CDN-Cache-Control", "Cloudflare-CDN-Cache-Control", "Surrogate-Control"}

func answerPreviewGate(w http.ResponseWriter, r *http.Request, previewGate *previewgate.Gate, named func(http.Header), now time.Time) (func(http.Header), bool) {
	if previewGate == nil {
		return func(http.Header) {}, true
	}
	request := previewgate.Request{
		Method: r.Method,
		Host:   hostOf(r.Host),
		Target: r.URL.RequestURI(),
		Header: previewgate.Header(r.Header),
	}
	verdict := previewGate.Check(request, now)
	if answer := verdict.Response; answer != nil {
		for name, values := range answer.Header {
			w.Header()[name] = values
		}
		named(w.Header())
		w.WriteHeader(answer.Status)
		_, _ = io.WriteString(w, answer.Body)
		return nil, false
	}
	r.Header = http.Header(verdict.Forward)
	return func(header http.Header) { keepPrivate(header, http.Header(verdict.ResponseHeader)) }, true
}

func keepPrivate(header http.Header, previewHeader http.Header) {
	for name, values := range previewHeader {
		header[name] = values
	}
	for _, override := range cacheOverrides {
		header.Del(override)
	}
	var kept []string
	private := false
	for _, value := range header.Values("Cache-Control") {
		for _, directive := range strings.Split(value, ",") {
			directive = strings.TrimSpace(directive)
			name, _, _ := strings.Cut(strings.ToLower(directive), "=")
			switch {
			case directive == "":
			case name == "s-maxage":
			case name == "public":
				kept, private = append(kept, "private"), true
			default:
				kept = append(kept, directive)
				private = private || name == "private" || name == "no-store"
			}
		}
	}
	if !private {
		kept = append(kept, "private")
	}
	header.Set("Cache-Control", strings.Join(slices.Compact(kept), ", "))
}
