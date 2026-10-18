package gate

import (
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

	statusNoContent    = 204
	statusSeeOther     = 303
	statusUnauthorized = 401
)

const (
	CookieName            = "__Host-ocel-session"
	LoginPath             = "/.ocel/login"
	HeaderBypass          = "x-ocel-bypass"
	HeaderSetBypassCookie = "x-ocel-set-bypass-cookie"
	SessionLifetime       = 7 * 24 * time.Hour
	CacheControl          = "private, no-store"
	RobotsTag             = "noindex"
	htmlContentType       = "text/html; charset=utf-8"
	signatureHexLen       = 2 * sha256.Size
	cookiePurpose         = "cookie"
	passwordPurpose       = "password"
	bypassPurpose         = "bypass"
	loginRealm            = `Basic realm="Ocel", charset="UTF-8"`
	loginScript           = `var p=document.getElementById("password");async function go(){var r=await fetch("` + LoginPath + `",{method:"POST",headers:{Authorization:"Basic "+btoa(String.fromCharCode(...new TextEncoder().encode(":"+p.value)))}});r.ok?location.assign(document.querySelector("main").dataset.next):document.getElementById("incorrect").hidden=!1}`
	loginPage             = `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Password required</title>` +
		`<main data-next="%NEXT%"><p id="incorrect" role="alert" hidden>Incorrect password.</p>` +
		`<label>Password <input id="password" type="password" autocomplete="current-password" autofocus onkeydown="event.key=='Enter'&&go()"></label>` +
		`<button onclick="go()">Continue</button>` +
		`<noscript><p><a href="` + LoginPath + `">Sign in with the browser prompt</a></p></noscript></main>` +
		`<script>` + loginScript + `</script>`
)

type Gate struct {
	Key           string   `json:"key"`
	PasswordHash  string   `json:"passwordHash"`
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

func isLogin(method, target string) bool {
	path, _, _ := strings.Cut(target, "?")
	return path == LoginPath && (method == methodPost || method == methodGet || method == methodHead)
}

func (g Gate) Check(req Request, now time.Time) Verdict {
	if isLogin(req.Method, req.Target) {
		return g.checkLogin(req, now)
	}
	path, _, _ := strings.Cut(req.Target, "?")

	hasBasic := g.hasBasicPassword(joinHeaderValues(req.Header, "Authorization"))
	hasBypass := g.hasBypassSecret(joinHeaderValues(req.Header, HeaderBypass))
	hasCookie := g.hasSession(req, now)

	if hasBypass && joinHeaderValues(req.Header, HeaderSetBypassCookie) == "true" && (req.Method == methodGet || req.Method == methodHead) {
		return g.redirect(sanitizeNext(req.Target), req.Host, now)
	}

	if !hasBasic && !hasBypass && !hasCookie && !g.allowsOptions(req.Method, path) {
		return answerLoginPage(sanitizeNext(req.Target), false)
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
	if g.hasBasicPassword(joinHeaderValues(req.Header, "Authorization")) {
		if req.Method == methodPost {
			return g.sessionStarted(req.Host, now)
		}
		return g.redirect("/", req.Host, now)
	}
	if req.Method == methodPost {
		return answerStatus(statusUnauthorized)
	}
	return answerLoginPage("/", true)
}

func (g Gate) hasBasicPassword(authorization string) bool {
	scheme, credentials, found := strings.Cut(authorization, " ")
	if !found || toLowerASCII(scheme) != "basic" {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.Trim(credentials, " \t"))
	if err != nil {
		return false
	}
	_, password, found := strings.Cut(string(decoded), ":")
	return found && g.HasPassword(password)
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
	header.Set("Set-Cookie", g.sessionCookie(host, now))
	return Verdict{Response: &Response{Status: statusSeeOther, Header: header}}
}

func (g Gate) sessionCookie(host string, now time.Time) string {
	return CookieName + "=" + g.SignSession(host, now) +
		"; Max-Age=" + strconv.Itoa(int(SessionLifetime/time.Second)) + "; Path=/; Secure; HttpOnly; SameSite=Lax"
}

func (g Gate) sessionStarted(host string, now time.Time) Verdict {
	header := newGateResponseHeader()
	header.Set("Set-Cookie", g.sessionCookie(host, now))
	return Verdict{Response: &Response{Status: statusNoContent, Header: header}}
}

func answerLoginPage(next string, prompt bool) Verdict {
	header := newGateResponseHeader()
	header.Set("Content-Type", htmlContentType)
	if prompt {
		header.Set("WWW-Authenticate", loginRealm)
	}
	body := strings.NewReplacer("%NEXT%", escapeHTML(next)).Replace(loginPage)
	return Verdict{Response: &Response{Status: statusUnauthorized, Header: header, Body: body}}
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
