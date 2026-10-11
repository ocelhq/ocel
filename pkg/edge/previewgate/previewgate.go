package previewgate

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	methodGet     = "GET"
	methodHead    = "HEAD"
	methodOptions = "OPTIONS"
	methodPost    = "POST"

	statusSeeOther              = 303
	statusUnauthorized          = 401
	statusRequestEntityTooLarge = 413
	statusUnsupportedMediaType  = 415
)

const (
	CookieName              = "__Host-ocel-preview"
	LoginPath               = "/.ocel/preview/login"
	HeaderBypass            = "x-ocel-preview-bypass"
	HeaderSetBypassCookie   = "x-ocel-set-bypass-cookie"
	SessionLifetime         = 7 * 24 * time.Hour
	LoginBodyMaxBytes       = 1024
	CacheControl            = "private, no-store"
	RobotsTag               = "noindex"
	formContentType         = "application/x-www-form-urlencoded"
	htmlContentType         = "text/html; charset=utf-8"
	signatureHexLen         = 2 * sha256.Size
	cookiePurpose           = "cookie"
	passwordPurpose         = "password"
	bypassPurpose           = "bypass"
	incorrectPasswordNotice = "Incorrect password."
)

type Gate struct {
	Key           string   `json:"key"`
	PasswordMAC   string   `json:"passwordMac"`
	BypassSecrets []string `json:"bypassSecrets"`
	AllowOptions  []string `json:"allowOptions"`
}

func (g Gate) MarshalJSON() ([]byte, error) {
	type listedGate Gate
	listed := listedGate(g)
	if listed.BypassSecrets == nil {
		listed.BypassSecrets = []string{}
	}
	if listed.AllowOptions == nil {
		listed.AllowOptions = []string{}
	}
	return json.Marshal(listed)
}

type Request struct {
	Method string
	Host   string
	Target string
	Header Header
	Body   []byte
}

type Response struct {
	Status int
	Header Header
	Body   string
}

type Verdict struct {
	Response       *Response
	Forward        Header
	ResponseHeader Header
}

func HashPassword(key, password string) string {
	return sign(key, passwordPurpose, password)
}

func IsLogin(method, target string) bool {
	path, _, _ := strings.Cut(target, "?")
	return method == methodPost && path == LoginPath
}

func (g Gate) Check(req Request, now time.Time) Verdict {
	if IsLogin(req.Method, req.Target) {
		return g.checkLogin(req, now)
	}
	path, _, _ := strings.Cut(req.Target, "?")

	hasBasic := g.hasPassword(joinHeaderValues(req.Header, "Authorization"))
	hasBypass := g.hasBypassSecret(joinHeaderValues(req.Header, HeaderBypass))
	hasCookie := g.hasSession(req, now)

	if hasBypass && joinHeaderValues(req.Header, HeaderSetBypassCookie) == "true" && (req.Method == methodGet || req.Method == methodHead) {
		return g.redirect(sanitizeNext(req.Target), req.Host, now)
	}

	if !hasBasic && !hasBypass && !hasCookie && !g.allowsOptions(req.Method, path) {
		return answerLoginForm(statusUnauthorized, sanitizeNext(req.Target), false)
	}

	forward := req.Header.Clone()
	forward.Del(HeaderBypass)
	forward.Del(HeaderSetBypassCookie)
	if hasBasic {
		forward.Del("Authorization")
	}
	dropSessionCookie(forward)
	return Verdict{Forward: forward, ResponseHeader: Header{"X-Robots-Tag": {RobotsTag}}}
}

func (g Gate) checkLogin(req Request, now time.Time) Verdict {
	if len(req.Body) > LoginBodyMaxBytes {
		return answerStatus(statusRequestEntityTooLarge)
	}
	mediaType, _, _ := strings.Cut(joinHeaderValues(req.Header, "Content-Type"), ";")
	if toLowerASCII(strings.Trim(mediaType, " \t")) != formContentType {
		return answerStatus(statusUnsupportedMediaType)
	}
	form := parseForm(req.Body)
	next := sanitizeNext(form["next"])
	if !g.isPassword(form["password"]) {
		return answerLoginForm(statusUnauthorized, next, true)
	}
	return g.redirect(next, req.Host, now)
}

func (g Gate) isPassword(password string) bool {
	return g.PasswordMAC != "" && utf8.ValidString(password) &&
		hmac.Equal([]byte(HashPassword(g.Key, password)), []byte(g.PasswordMAC))
}

func (g Gate) hasPassword(authorization string) bool {
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

func (g Gate) hasBypassSecret(value string) bool {
	if value == "" || !isASCII(value) {
		return false
	}
	want := sign(g.Key, bypassPurpose, value)
	matched := false
	for _, secret := range g.BypassSecrets {
		if secret != "" && hmac.Equal([]byte(sign(g.Key, bypassPurpose, secret)), []byte(want)) {
			matched = true
		}
	}
	return matched
}

func (g Gate) hasSession(req Request, now time.Time) bool {
	for _, value := range readSessionCookies(req.Header) {
		expiry, signature, found := strings.Cut(value, ".")
		if !found || len(signature) != signatureHexLen || !isLowerHex(signature) || !isDigits(expiry) {
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

func (g Gate) allowsOptions(method, path string) bool {
	if method != methodOptions || strings.ContainsAny(path, `%\`) || strings.Contains(path, "..") {
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

func (g Gate) signSession(host, expiry string) string {
	return sign(g.Key, cookiePurpose, toLowerASCII(host)+"\n"+expiry)
}

func (g Gate) SignSession(host string, now time.Time) string {
	expiry := strconv.FormatInt(now.Add(SessionLifetime).Unix(), 10)
	return expiry + "." + g.signSession(host, expiry)
}

func (g Gate) redirect(location, host string, now time.Time) Verdict {
	header := newGateResponseHeader()
	header.Set("Location", location)
	header.Set("Set-Cookie", CookieName+"="+g.SignSession(host, now)+
		"; Max-Age="+strconv.Itoa(int(SessionLifetime/time.Second))+"; Path=/; Secure; HttpOnly; SameSite=Lax")
	return Verdict{Response: &Response{Status: statusSeeOther, Header: header}}
}

func answerLoginForm(status int, next string, incorrect bool) Verdict {
	notice := ""
	if incorrect {
		notice = `<p role="alert">` + incorrectPasswordNotice + `</p>`
	}
	header := newGateResponseHeader()
	header.Set("Content-Type", htmlContentType)
	body := `<!doctype html><html lang="en"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width,initial-scale=1"><meta name="robots" content="noindex">` +
		`<title>Preview protected</title></head><body>` + notice +
		`<form method="post" action="` + LoginPath + `">` +
		`<label>Password <input type="password" name="password" autocomplete="current-password" autofocus required></label>` +
		`<input type="hidden" name="next" value="` + escapeHTML(next) + `">` +
		`<button type="submit">Continue</button></form></body></html>`
	return Verdict{Response: &Response{Status: status, Header: header, Body: body}}
}

func answerStatus(status int) Verdict {
	return Verdict{Response: &Response{Status: status, Header: newGateResponseHeader()}}
}

func newGateResponseHeader() Header {
	return Header{
		"Cache-Control": {CacheControl},
		"X-Robots-Tag":  {RobotsTag},
	}
}

func sign(key, purpose, input string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(purpose + "\n" + input))
	return hex.EncodeToString(mac.Sum(nil))
}

func joinHeaderValues(header Header, name string) string {
	return strings.Join(header.Values(name), ", ")
}

func sanitizeNext(target string) string {
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") || strings.HasPrefix(target, `/\`) {
		return "/"
	}
	for _, r := range target {
		if r < 0x20 || r >= 0x7f {
			return "/"
		}
	}
	if path, _, _ := strings.Cut(target, "?"); path == LoginPath {
		return "/"
	}
	return target
}

func parseForm(body []byte) map[string]string {
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

var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")

func escapeHTML(s string) string { return htmlEscaper.Replace(s) }

func readCookiePairs(header Header) []string {
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

func readSessionCookies(header Header) []string {
	var values []string
	for _, pair := range readCookiePairs(header) {
		if name, value, _ := strings.Cut(pair, "="); name == CookieName {
			values = append(values, value)
		}
	}
	return values
}

func dropSessionCookie(header Header) {
	var kept []string
	for _, pair := range readCookiePairs(header) {
		if name, _, _ := strings.Cut(pair, "="); name != CookieName {
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
