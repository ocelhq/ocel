package previewlogin

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/previewgate"
)

type Login struct {
	gate       *previewgate.Gate
	unreadable bool
}

func FromEnv(env []string) (*Login, []string) {
	login := &Login{}
	kept := make([]string, 0, len(env))
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		if name != edge.PreviewGateVar {
			kept = append(kept, entry)
			continue
		}
		var gate previewgate.Gate
		if err := json.Unmarshal([]byte(value), &gate); err != nil || gate.Key == "" {
			login.gate, login.unreadable = nil, true
			continue
		}
		login.gate, login.unreadable = &gate, false
	}
	return login, kept
}

func IsLogin(method, path string) bool {
	return method == http.MethodPost && path == previewgate.LoginPath
}

func ReadBody(body io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(body, previewgate.LoginBodyMaxBytes+1))
}

func (l *Login) Answer(req previewgate.Request, now time.Time) previewgate.Response {
	if verdict := l.Check(req, now); verdict.Response != nil {
		return *verdict.Response
	}
	return refuse(http.StatusForbidden, "the preview gate answered no login")
}

func (l *Login) Check(req previewgate.Request, now time.Time) previewgate.Verdict {
	switch {
	case l != nil && l.unreadable:
		return refuseVerdict(http.StatusServiceUnavailable, "this release could not read the keys that gate its preview")
	case l == nil || l.gate == nil:
		return refuseVerdict(http.StatusForbidden, "this release holds no keys to check a preview password against")
	}
	return l.gate.Check(req, now)
}

func refuseVerdict(status int, body string) previewgate.Verdict {
	answer := refuse(status, body)
	return previewgate.Verdict{Response: &answer}
}

func refuse(status int, body string) previewgate.Response {
	return previewgate.Response{
		Status: status,
		Header: previewgate.Header{
			"Content-Type":  {"text/plain; charset=utf-8"},
			"Cache-Control": {"private, no-store"},
			"X-Robots-Tag":  {"noindex"},
		},
		Body: body,
	}
}
