package edge

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	PreviewCookieName              = "__Host-ocel-preview"
	PreviewLoginPath               = "/.ocel/preview/login"
	HeaderPreviewBypass            = "x-ocel-preview-bypass"
	HeaderPreviewSetBypassCookie   = "x-ocel-set-bypass-cookie"
	PreviewSessionLifetime         = 7 * 24 * time.Hour
	PreviewLoginBodyMaxBytes       = 1024
	previewGateCacheControl        = "private, no-store"
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

	hasBasic := g.hasPassword(joinHeaderValues(req.Header, "Authorization"))
	hasBypass := g.hasBypassSecret(joinHeaderValues(req.Header, HeaderPreviewBypass))
	hasCookie := g.hasSession(req, now)

	if hasBypass && joinHeaderValues(req.Header, HeaderPreviewSetBypassCookie) == "true" && (req.Method == http.MethodGet || req.Method == http.MethodHead) {
		return g.redirect(sanitizePreviewNext(req.Target), req.Host, now)
	}

	if !hasBasic && !hasBypass && !hasCookie && !g.allowsOptions(req.Method, path) {
		return answerLoginForm(http.StatusUnauthorized, sanitizePreviewNext(req.Target), false)
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
		return answerStatus(http.StatusRequestEntityTooLarge)
	}
	mediaType, _, _ := strings.Cut(joinHeaderValues(req.Header, "Content-Type"), ";")
	if toLowerASCII(strings.Trim(mediaType, " \t")) != previewFormContentType {
		return answerStatus(http.StatusUnsupportedMediaType)
	}
	form := parsePreviewForm(req.Body)
	next := sanitizePreviewNext(form["next"])
	if !g.isPassword(form["password"]) {
		return answerLoginForm(http.StatusUnauthorized, next, true)
	}
	return g.redirect(next, req.Host, now)
}

func (g PreviewGate) isPassword(password string) bool {
	return g.PasswordMAC != "" && utf8.ValidString(password) &&
		hmac.Equal([]byte(HashPreviewPassword(g.Key, password)), []byte(g.PasswordMAC))
}

func (g PreviewGate) hasPassword(authorization string) bool {
	scheme, credentials, found := strings.Cut(authorization, " ")
	if !found || toLowerASCII(scheme) != "basic" {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.Trim(credentials, " \t"))
	if err != nil {
		return false
	}
	_, password, found := strings.Cut(string(decoded), ":")
	return found && g.isPassword(password)
}

func (g PreviewGate) hasBypassSecret(value string) bool {
	if value == "" || !isASCII(value) {
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
		if hmac.Equal([]byte(g.signSession(req.Host, expiry)), []byte(signature)) {
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

func (g PreviewGate) signSession(host, expiry string) string {
	return signPreview(g.Key, previewCookiePurpose, toLowerASCII(host)+"\n"+expiry)
}

func (g PreviewGate) redirect(location, host string, now time.Time) PreviewVerdict {
	expiry := strconv.FormatInt(now.Add(PreviewSessionLifetime).Unix(), 10)
	header := newGateResponseHeader()
	header.Set("Location", location)
	header.Set("Set-Cookie", fmt.Sprintf("%s=%s.%s; Max-Age=%d; Path=/; Secure; HttpOnly; SameSite=Lax",
		PreviewCookieName, expiry, g.signSession(host, expiry), int(PreviewSessionLifetime/time.Second)))
	return PreviewVerdict{Response: &PreviewResponse{Status: http.StatusSeeOther, Header: header}}
}

func answerLoginForm(status int, next string, incorrect bool) PreviewVerdict {
	notice := ""
	if incorrect {
		notice = `<p role="alert">` + previewIncorrectPasswordNotice + `</p>`
	}
	header := newGateResponseHeader()
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

func answerStatus(status int) PreviewVerdict {
	return PreviewVerdict{Response: &PreviewResponse{Status: status, Header: newGateResponseHeader()}}
}

func newGateResponseHeader() http.Header {
	return http.Header{
		"Cache-Control": {previewGateCacheControl},
		"X-Robots-Tag":  {previewRobotsTag},
	}
}

func signPreview(key, purpose, input string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(purpose + "\n" + input))
	return hex.EncodeToString(mac.Sum(nil))
}

func joinHeaderValues(header http.Header, name string) string {
	return strings.Join(header.Values(name), ", ")
}

func sanitizePreviewNext(target string) string {
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") || strings.HasPrefix(target, `/\`) {
		return "/"
	}
	for _, r := range target {
		if r < 0x20 || r >= 0x7f {
			return "/"
		}
	}
	if path, _, _ := strings.Cut(target, "?"); path == PreviewLoginPath {
		return "/"
	}
	return target
}

func parsePreviewForm(body []byte) map[string]string {
	form := map[string]string{}
	for _, pair := range bytes.Split(body, []byte("&")) {
		if len(pair) == 0 {
			continue
		}
		name, value, _ := bytes.Cut(pair, []byte("="))
		key := decodeFormComponent(name)
		if _, seen := form[key]; !seen {
			form[key] = decodeFormComponent(value)
		}
	}
	return form
}

func decodeFormComponent(component []byte) string {
	decoded := make([]byte, 0, len(component))
	for i := 0; i < len(component); i++ {
		if component[i] == '+' {
			decoded = append(decoded, ' ')
			continue
		}
		if component[i] == '%' && i+2 < len(component) {
			if escaped, ok := decodeHexByte(component[i+1 : i+3]); ok {
				decoded = append(decoded, escaped)
				i += 2
				continue
			}
		}
		decoded = append(decoded, component[i])
	}
	return string(decoded)
}

func decodeHexByte(digits []byte) (byte, bool) {
	var decoded [1]byte
	_, err := hex.Decode(decoded[:], digits)
	return decoded[0], err == nil
}

var previewHTMLEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")

func escapePreviewHTML(s string) string { return previewHTMLEscaper.Replace(s) }

func readCookiePairs(header http.Header) []string {
	var pairs []string
	for _, line := range header.Values("Cookie") {
		for _, pair := range strings.Split(line, ";") {
			if pair = strings.Trim(pair, " \t"); pair != "" {
				pairs = append(pairs, pair)
			}
		}
	}
	return pairs
}

func readPreviewCookies(header http.Header) []string {
	var values []string
	for _, pair := range readCookiePairs(header) {
		if name, value, _ := strings.Cut(pair, "="); name == PreviewCookieName {
			values = append(values, value)
		}
	}
	return values
}

func dropPreviewCookie(header http.Header) {
	var kept []string
	for _, pair := range readCookiePairs(header) {
		if name, _, _ := strings.Cut(pair, "="); name != PreviewCookieName {
			kept = append(kept, pair)
		}
	}
	if len(kept) == 0 {
		header.Del("Cookie")
		return
	}
	header.Set("Cookie", strings.Join(kept, "; "))
}

func toLowerASCII(s string) string {
	lowered := []byte(s)
	for i, b := range lowered {
		if 'A' <= b && b <= 'Z' {
			lowered[i] = b + 'a' - 'A'
		}
	}
	return string(lowered)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
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
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
