package host

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type credential struct {
	AccessKeyID string
	SecretKey   string
	Region      string
}

const (
	signingAlgorithm = "AWS4-HMAC-SHA256"
	signingService   = "s3"
	signingTerminal  = "aws4_request"

	amzDateLayout  = "20060102T150405Z"
	scopeDateLayou = "20060102"
)

func signRequest(req *http.Request, held credential, payloadHash string, now time.Time) {
	now = now.UTC()
	req.Header.Set("Host", req.URL.Host)
	req.Header.Set("X-Amz-Date", now.Format(amzDateLayout))
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	names, canonicalHeaders := canonicalHeaders(req)
	signed := strings.Join(names, ";")
	canonical := strings.Join([]string{
		req.Method,
		canonicalPath(req.URL),
		canonicalQuery(req.URL),
		canonicalHeaders,
		signed,
		payloadHash,
	}, "\n")

	scope := strings.Join([]string{
		now.Format(scopeDateLayou), held.Region, signingService, signingTerminal,
	}, "/")
	hashed := sha256.Sum256([]byte(canonical))
	toSign := strings.Join([]string{
		signingAlgorithm,
		now.Format(amzDateLayout),
		scope,
		hex.EncodeToString(hashed[:]),
	}, "\n")

	key := hmacOf(hmacOf(hmacOf(hmacOf(
		[]byte("AWS4"+held.SecretKey), now.Format(scopeDateLayou)),
		held.Region), signingService), signingTerminal)

	req.Header.Set("Authorization", signingAlgorithm+
		" Credential="+held.AccessKeyID+"/"+scope+
		", SignedHeaders="+signed+
		", Signature="+hex.EncodeToString(hmacOf(key, toSign)))
}

const unsignedPayload = "UNSIGNED-PAYLOAD"

func presignURL(method string, at *url.URL, held credential, expiry time.Duration, now time.Time) string {
	now = now.UTC()
	scope := strings.Join([]string{
		now.Format(scopeDateLayou), held.Region, signingService, signingTerminal,
	}, "/")

	signed := *at
	values := at.Query()
	values.Set("X-Amz-Algorithm", signingAlgorithm)
	values.Set("X-Amz-Credential", held.AccessKeyID+"/"+scope)
	values.Set("X-Amz-Date", now.Format(amzDateLayout))
	values.Set("X-Amz-Expires", strconv.Itoa(int(expiry/time.Second)))
	values.Set("X-Amz-SignedHeaders", "host")
	signed.RawQuery = values.Encode()
	signed.RawQuery = canonicalQuery(&signed)

	canonical := strings.Join([]string{
		method,
		canonicalPath(at),
		signed.RawQuery,
		"host:" + at.Host + "\n",
		"host",
		unsignedPayload,
	}, "\n")
	hashed := sha256.Sum256([]byte(canonical))
	toSign := strings.Join([]string{
		signingAlgorithm, now.Format(amzDateLayout), scope, hex.EncodeToString(hashed[:]),
	}, "\n")

	signed.RawQuery += "&X-Amz-Signature=" + hex.EncodeToString(hmacOf(signingKey(held, now), toSign))
	return signed.String()
}

func signingKey(held credential, now time.Time) []byte {
	return hmacOf(hmacOf(hmacOf(hmacOf(
		[]byte("AWS4"+held.SecretKey), now.UTC().Format(scopeDateLayou)),
		held.Region), signingService), signingTerminal)
}

func hmacOf(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

func canonicalHeaders(req *http.Request) ([]string, string) {
	held := map[string]string{"host": req.URL.Host}
	for name, values := range req.Header {
		lower := strings.ToLower(name)
		if lower == "authorization" || lower == "content-length" || lower == "user-agent" {
			continue
		}
		held[lower] = strings.Join(trimmed(values), ",")
	}
	names := make([]string, 0, len(held))
	for name := range held {
		names = append(names, name)
	}
	sort.Strings(names)

	var canonical strings.Builder
	for _, name := range names {
		canonical.WriteString(name)
		canonical.WriteString(":")
		canonical.WriteString(held[name])
		canonical.WriteString("\n")
	}
	return names, canonical.String()
}

func trimmed(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strings.Join(strings.Fields(value), " "))
	}
	return out
}

func canonicalPath(at *url.URL) string {
	if at.Path == "" {
		return "/"
	}
	segments := strings.Split(at.Path, "/")
	for i, segment := range segments {
		segments[i] = uriEscape(segment)
	}
	return strings.Join(segments, "/")
}

func canonicalQuery(at *url.URL) string {
	values := at.Query()
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		held := values[key]
		sort.Strings(held)
		for _, value := range held {
			pairs = append(pairs, uriEscape(key)+"="+uriEscape(value))
		}
	}
	return strings.Join(pairs, "&")
}

func uriEscape(raw string) string {
	var out strings.Builder
	for _, b := range []byte(raw) {
		switch {
		case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9',
			b == '-', b == '_', b == '.', b == '~':
			out.WriteByte(b)
		default:
			out.WriteString("%")
			out.WriteString(strings.ToUpper(hex.EncodeToString([]byte{b})))
		}
	}
	return out.String()
}
