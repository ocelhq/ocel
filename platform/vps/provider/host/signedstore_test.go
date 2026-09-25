package host

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit/enginetest"
)

type signedStore struct {
	Endpoint    string
	Region      string
	Bucket      string
	AccessKeyID string
	SecretKey   string
	PathStyle   bool
}

const probeBody = "ocel"

var storeWrote = []string{"200", "201", "204"}

var uploadIDInAnswer = regexp.MustCompile(`<UploadId>([^<]+)</UploadId>`)

type probeCall struct {
	name    string
	method  string
	url     string
	headers map[string]string
	body    []byte
	form    []string
	capture bool
}

func (e signedStore) at(key, query string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSuffix(e.Endpoint, "/"))
	if err != nil {
		return nil, err
	}
	if e.PathStyle {
		parsed.Path = "/" + e.Bucket
	} else {
		parsed.Host = e.Bucket + "." + parsed.Host
	}
	if key != "" {
		parsed.Path = strings.TrimSuffix(parsed.Path, "/") + "/" + key
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	parsed.RawQuery = query
	return parsed, nil
}

func (e signedStore) held() credential {
	return credential{AccessKeyID: e.AccessKeyID, SecretKey: e.SecretKey, Region: e.Region}
}

func (e signedStore) call(name, method, key, query string, headers map[string]string, body []byte, capture bool, now time.Time) (probeCall, error) {
	at, err := e.at(key, query)
	if err != nil {
		return probeCall{}, err
	}
	req, err := http.NewRequest(method, at.String(), strings.NewReader(string(body)))
	if err != nil {
		return probeCall{}, err
	}
	req.ContentLength = int64(len(body))
	for header, value := range headers {
		req.Header.Set(header, value)
	}
	payload := sha256.Sum256(body)
	signRequest(req, e.held(), hex.EncodeToString(payload[:]), now)

	sent := map[string]string{}
	for _, header := range sortedHeaderNames(req.Header) {
		sent[header] = req.Header.Get(header)
	}
	return probeCall{
		name: name, method: method, url: at.String(),
		headers: sent, body: body, capture: capture,
	}, nil
}

func probeScript(calls []probeCall) string {
	written := strings.Builder{}
	written.WriteString("set -u\n" +
		"tmp=$(mktemp -d)\n" +
		"trap 'rm -rf \"$tmp\"' EXIT\n")
	for i, call := range calls {
		held := "\"$tmp/" + strconv.Itoa(i) + "\""
		argv := []string{"curl", "--silent", "--show-error", "--location",
			"--write-out", "%{http_code}", "--request", call.method}
		for _, header := range sortedHeaderNames(http.Header(headerOf(call.headers))) {
			argv = append(argv, "--header", header+": "+call.headers[header])
		}
		for _, field := range call.form {
			argv = append(argv, "--form", field)
		}
		if len(call.form) == 0 && len(call.body) > 0 {
			argv = append(argv, "--data-binary", "@-")
		}
		argv = append(argv, call.url)

		fed := ""
		if len(call.body) > 0 {
			fed = "printf '%s' " + quoted(base64.StdEncoding.EncodeToString(call.body)) + " | base64 -d | "
		}
		written.WriteString("answered=$(" + fed + words(argv) +
			" --output " + held + " 2>/dev/null || printf 'no-answer')\n")
		written.WriteString("printf '%s=%s\\n' " + quoted(call.name) + " \"$answered\"\n")
		if call.capture {
			written.WriteString("printf '%s.body=%s\\n' " + quoted(call.name) +
				" \"$(base64 " + held + " 2>/dev/null | tr -d '\\n')\"\n")
		}
	}
	return written.String()
}

func headerOf(held map[string]string) map[string][]string {
	out := make(map[string][]string, len(held))
	for name, value := range held {
		out[name] = []string{value}
	}
	return out
}

func aSignedStore(t *testing.T, store enginetest.Store, bucket string) signedStore {
	t.Helper()
	aBucketOn(t, store, bucket)
	return signedStore{
		Endpoint:    store.Endpoint,
		Region:      store.Region,
		Bucket:      bucket,
		AccessKeyID: store.AccessKeyID,
		SecretKey:   store.SecretKey,
		PathStyle:   true,
	}
}

func hereRuns(t *testing.T) storeShell {
	t.Helper()
	return func(what, script string) (string, error) {
		run := exec.Command("sh", "-c", script)
		out, err := run.Output()
		if err != nil {
			t.Fatalf("%s: %v\n%s", what, err, script)
		}
		return string(out), nil
	}
}
