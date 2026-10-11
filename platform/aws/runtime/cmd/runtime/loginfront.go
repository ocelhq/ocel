package main

import (
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	"github.com/ocelhq/ocel/pkg/runtime/previewlogin"
)

type loginFront struct {
	login *previewlogin.Login
	guard *originguard.Guard
}

func readLoginFront() loginFront {
	login, _ := previewlogin.FromEnv(os.Environ())
	guard, _ := originguard.GuardFromEnv(os.Environ())
	_ = os.Unsetenv(edge.PreviewGateVar)
	return loginFront{login: login, guard: guard}
}

func (f loginFront) answer(ev *httpEvent, now time.Time) *http.Response {
	header := ev.header()
	if !f.guard.Admits(&http.Request{Header: header}) {
		return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{"Cache-Control": {"private, no-store"}, "X-Robots-Tag": {"noindex"}}, Body: http.NoBody}
	}
	body, _ := previewlogin.ReadBody(ev.bodyReader())
	target := ev.path()
	if query := ev.query(); query != "" {
		target += "?" + query
	}
	answer := f.login.Answer(edge.PreviewRequest{
		Method: ev.method(),
		Host:   publicHost(header),
		Target: target,
		Header: header,
		Body:   body,
	}, now)
	return &http.Response{StatusCode: answer.Status, Header: answer.Header, Body: io.NopCloser(strings.NewReader(answer.Body))}
}

func (ev *httpEvent) bodyReader() io.Reader {
	if ev.IsBase64Encoded {
		return base64.NewDecoder(base64.StdEncoding, strings.NewReader(ev.Body))
	}
	return strings.NewReader(ev.Body)
}
