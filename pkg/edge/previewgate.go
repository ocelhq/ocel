package edge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	PreviewCookieName              = "__Host-ocel-preview"
	PreviewLoginPath               = "/.ocel/preview/login"
	HeaderPreviewBypass            = "x-ocel-preview-bypass"
	HeaderPreviewSetBypassCookie   = "x-ocel-set-bypass-cookie"
	PreviewSessionLifetime         = 7 * 24 * time.Hour
	PreviewLoginBodyMaxBytes       = 1024
	previewRefusalCacheControl     = "private, no-store"
	previewRobotsTag               = "noindex"
	previewFormContentType         = "application/x-www-form-urlencoded"
	previewHTMLContentType         = "text/html; charset=utf-8"
	previewSignatureHexLen         = 2 * sha256.Size
	previewCookiePurpose           = "cookie"
	previewPasswordPurpose         = "password"
	previewBypassPurpose           = "bypass"
	previewIncorrectPasswordNotice = "Incorrect password."
)

type PreviewGate struct {
	Key           string   `json:"key"`
	PasswordMAC   string   `json:"passwordMac"`
	BypassSecrets []string `json:"bypassSecrets"`
	AllowOptions  []string `json:"allowOptions"`
}

type PreviewRequest struct {
	Method string
	Host   string
	Target string
	Header http.Header
	Body   []byte
}

type PreviewResponse struct {
	Status int
	Header http.Header
	Body   string
}

type PreviewVerdict struct {
	Response       *PreviewResponse
	Forward        http.Header
	ResponseHeader http.Header
}

func HashPreviewPassword(key, password string) string {
	return signPreview(key, previewPasswordPurpose, password)
}

func (g PreviewGate) Check(req PreviewRequest, now time.Time) PreviewVerdict {
	path, _, _ := strings.Cut(req.Target, "?")

	if req.Method == http.MethodPost && path == PreviewLoginPath {
		return g.checkLogin(req, now)
	}

	hasBasic := g.hasPassword(req.Header.Get("Authorization"))
	hasBypass := g.hasBypassSecret(req.Header.Get(HeaderPreviewBypass))
	hasCookie := g.hasSession(req, now)

	if hasBypass && req.Header.Get(HeaderPreviewSetBypassCookie) == "true" && (req.Method == http.MethodGet || req.Method == http.MethodHead) {
		return g.redirect(safePreviewNext(req.Target), req.Host, now)
	}

	if !(hasBasic || hasBypass || hasCookie || g.allowsOptions(req.Method, path)) {
		return g.refuse(http.StatusUnauthorized, safePreviewNext(req.Target), false)
	}

	forward := req.Header.Clone()
	forward.Del(HeaderPreviewBypass)
	forward.Del(HeaderPreviewSetBypassCookie)
	if hasBasic {
		forward.Del("Authorization")
	}
	dropPreviewCookie(forward)
	return PreviewVerdict{Forward: forward, ResponseHeader: http.Header{"X-Robots-Tag": {previewRobotsTag}}}
}

func (g PreviewGate) checkLogin(req PreviewRequest, now time.Time) PreviewVerdict {
	if len(req.Body) > PreviewLoginBodyMaxBytes {
		return previewBare(http.StatusRequestEntityTooLarge)
	}
	mediaType, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil || mediaType != previewFormContentType {
		return previewBare(http.StatusUnsupportedMediaType)
	}
	form, _ := url.ParseQuery(string(req.Body))
	next := safePreviewNext(form.Get("next"))
	if !g.isPassword(form.Get("password")) {
		return g.refuse(http.StatusUnauthorized, next, true)
	}
	return g.redirect(next, req.Host, now)
}

func (g PreviewGate) isPassword(password string) bool {
	return g.PasswordMAC != "" && hmac.Equal([]byte(HashPreviewPassword(g.Key, password)), []byte(g.PasswordMAC))
}

func (g PreviewGate) hasPassword(authorization string) bool {
	scheme, credentials, found := strings.Cut(authorization, " ")
	if !found || !strings.EqualFold(scheme, "Basic") {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(credentials))
	if err != nil {
		return false
	}
	_, password, found := strings.Cut(string(decoded), ":")
	return found && g.isPassword(password)
}

func (g PreviewGate) hasBypassSecret(value string) bool {
	if value == "" {
		return false
	}
	want := signPreview(g.Key, previewBypassPurpose, value)
	matched := false
	for _, secret := range g.BypassSecrets {
		if secret != "" && hmac.Equal([]byte(signPreview(g.Key, previewBypassPurpose, secret)), []byte(want)) {
			matched = true
		}
	}
	return matched
}

func (g PreviewGate) hasSession(req PreviewRequest, now time.Time) bool {
	for _, value := range readPreviewCookies(req.Header) {
		expiry, signature, found := strings.Cut(value, ".")
		if !found || len(signature) != previewSignatureHexLen || !isLowerHex(signature) || !isDigits(expiry) {
			continue
		}
		seconds, err := strconv.ParseInt(expiry, 10, 64)
		if err != nil || seconds <= now.Unix() {
			continue
		}
		want := signPreview(g.Key, previewCookiePurpose, strings.ToLower(req.Host)+"\n"+expiry)
		if hmac.Equal([]byte(want), []byte(signature)) {
			return true
		}
	}
	return false
}

func (g PreviewGate) allowsOptions(method, path string) bool {
	if method != http.MethodOptions || strings.ContainsAny(path, `%\`) || strings.Contains(path, "..") {
		return false
	}
	for _, prefix := range g.AllowOptions {
		prefix = strings.TrimSuffix(prefix, "/")
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func (g PreviewGate) redirect(location, host string, now time.Time) PreviewVerdict {
	expiry := strconv.FormatInt(now.Add(PreviewSessionLifetime).Unix(), 10)
	signature := signPreview(g.Key, previewCookiePurpose, strings.ToLower(host)+"\n"+expiry)
	header := previewRefusalHeader()
	header.Set("Location", location)
	header.Set("Set-Cookie", fmt.Sprintf("%s=%s.%s; Max-Age=%d; Path=/; Secure; HttpOnly; SameSite=Lax",
		PreviewCookieName, expiry, signature, int(PreviewSessionLifetime/time.Second)))
	return PreviewVerdict{Response: &PreviewResponse{Status: http.StatusSeeOther, Header: header}}
}

func (g PreviewGate) refuse(status int, next string, incorrect bool) PreviewVerdict {
	notice := ""
	if incorrect {
		notice = `<p role="alert">` + previewIncorrectPasswordNotice + `</p>`
	}
	header := previewRefusalHeader()
	header.Set("Content-Type", previewHTMLContentType)
	body := `<!doctype html><html lang="en"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width,initial-scale=1"><meta name="robots" content="noindex">` +
		`<title>Preview protected</title></head><body>` + notice +
		`<form method="post" action="` + PreviewLoginPath + `">` +
		`<label>Password <input type="password" name="password" autocomplete="current-password" autofocus required></label>` +
		`<input type="hidden" name="next" value="` + escapePreviewHTML(next) + `">` +
		`<button type="submit">Continue</button></form></body></html>`
	return PreviewVerdict{Response: &PreviewResponse{Status: status, Header: header, Body: body}}
}

func previewBare(status int) PreviewVerdict {
	return PreviewVerdict{Response: &PreviewResponse{Status: status, Header: previewRefusalHeader()}}
}

func previewRefusalHeader() http.Header {
	return http.Header{
		"Cache-Control": {previewRefusalCacheControl},
		"X-Robots-Tag":  {previewRobotsTag},
	}
}

func signPreview(key, purpose, input string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(purpose + "\n" + input))
	return hex.EncodeToString(mac.Sum(nil))
}

func safePreviewNext(target string) string {
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") || strings.HasPrefix(target, `/\`) {
		return "/"
	}
	for _, r := range target {
		if r < 0x20 || r == 0x7f {
			return "/"
		}
	}
	if path, _, _ := strings.Cut(target, "?"); path == PreviewLoginPath {
		return "/"
	}
	return target
}

var previewHTMLEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")

func escapePreviewHTML(s string) string { return previewHTMLEscaper.Replace(s) }

func readPreviewCookies(header http.Header) []string {
	var values []string
	for _, line := range header.Values("Cookie") {
		for _, pair := range strings.Split(line, ";") {
			name, value, _ := strings.Cut(strings.TrimSpace(pair), "=")
			if name == PreviewCookieName {
				values = append(values, value)
			}
		}
	}
	return values
}

func dropPreviewCookie(header http.Header) {
	lines := header.Values("Cookie")
	if len(lines) == 0 {
		return
	}
	var kept []string
	for _, line := range lines {
		for _, pair := range strings.Split(line, ";") {
			pair = strings.TrimSpace(pair)
			if name, _, _ := strings.Cut(pair, "="); name != PreviewCookieName && pair != "" {
				kept = append(kept, pair)
			}
		}
	}
	if len(kept) == 0 {
		header.Del("Cookie")
		return
	}
	header.Set("Cookie", strings.Join(kept, "; "))
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isLowerHex(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
