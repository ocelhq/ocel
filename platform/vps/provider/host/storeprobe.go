package host

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

type ExternalStore struct {
	Endpoint    string
	Region      string
	Bucket      string
	AccessKeyID string
	SecretKey   string
	PathStyle   bool
}

type StoreProbe struct {
	PostPolicies bool
}

const (
	probePrefix   = constants.ReservedKeyPrefix + "probe/"
	probeOrigin   = "https://probe.ocel.invalid"
	probeBody     = "ocel"
	probeExpiry   = 5 * time.Minute
	probeMaxBytes = 1 << 20
)

type probeCall struct {
	name    string
	method  string
	url     string
	headers map[string]string
	body    []byte
	form    []string
	capture bool
}

type probeAnswer struct {
	code string
	body []byte
}

func (e ExternalStore) at(key, query string) (*url.URL, error) {
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

func (e ExternalStore) held() credential {
	return credential{AccessKeyID: e.AccessKeyID, SecretKey: e.SecretKey, Region: e.Region}
}

func (e ExternalStore) call(name, method, key, query string, headers map[string]string, body []byte, capture bool, now time.Time) (probeCall, error) {
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

func (e ExternalStore) presigned(name, method, key string, capture bool, body []byte, now time.Time) (probeCall, error) {
	at, err := e.at(key, "")
	if err != nil {
		return probeCall{}, err
	}
	return probeCall{
		name: name, method: method, body: body, capture: capture,
		url: presignURL(method, at, e.held(), probeExpiry, now),
	}, nil
}

func (e ExternalStore) postForm(name, key string, now time.Time) (probeCall, error) {
	at, err := e.at("", "")
	if err != nil {
		return probeCall{}, err
	}
	now = now.UTC()
	scope := strings.Join([]string{
		now.Format(scopeDateLayou), e.Region, signingService, signingTerminal,
	}, "/")
	fields := map[string]string{
		"key":              key,
		"x-amz-algorithm":  signingAlgorithm,
		"x-amz-credential": e.AccessKeyID + "/" + scope,
		"x-amz-date":       now.Format(amzDateLayout),
	}
	policy, err := json.Marshal(map[string]any{
		"expiration": now.Add(probeExpiry).Format(time.RFC3339),
		"conditions": []any{
			map[string]string{"bucket": e.Bucket},
			map[string]string{"key": key},
			map[string]string{"x-amz-algorithm": signingAlgorithm},
			map[string]string{"x-amz-credential": fields["x-amz-credential"]},
			map[string]string{"x-amz-date": fields["x-amz-date"]},
			[]any{"content-length-range", 0, probeMaxBytes},
		},
	})
	if err != nil {
		return probeCall{}, err
	}
	encoded := base64.StdEncoding.EncodeToString(policy)
	fields["policy"] = encoded
	fields["x-amz-signature"] = hex.EncodeToString(hmacOf(signingKey(e.held(), now), encoded))

	form := make([]string, 0, len(fields)+1)
	for _, field := range []string{"key", "x-amz-algorithm", "x-amz-credential", "x-amz-date", "policy", "x-amz-signature"} {
		form = append(form, field+"="+fields[field])
	}
	form = append(form, "file=@-;filename=probe")
	return probeCall{name: name, method: http.MethodPost, url: at.String(), form: form, body: []byte(probeBody)}, nil
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

func readProbe(said string) map[string]probeAnswer {
	answers := map[string]probeAnswer{}
	for line := range strings.Lines(said) {
		name, value, cut := strings.Cut(strings.TrimSpace(line), "=")
		if !cut {
			continue
		}
		if held, is := strings.CutSuffix(name, ".body"); is {
			decoded, err := base64.StdEncoding.DecodeString(value)
			if err != nil {
				continue
			}
			answer := answers[held]
			answer.body = decoded
			answers[held] = answer
			continue
		}
		answer := answers[name]
		answer.code = value
		answers[name] = answer
	}
	return answers
}

func answeredWith(answers map[string]probeAnswer, name string, codes ...string) bool {
	held, said := answers[name]
	if !said {
		return false
	}
	for _, code := range codes {
		if held.code == code {
			return true
		}
	}
	return false
}

var storeWrote = []string{"200", "201", "204"}

var uploadIDInAnswer = regexp.MustCompile(`<UploadId>([^<]+)</UploadId>`)

type probeRunner func(what, script string) (string, error)

func probeRoot() (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return probePrefix + hex.EncodeToString(raw) + "/", nil
}

type corsPlan struct {
	body   []byte
	origin string
	drop   bool
	read   bool
	said   string
}

var originInAnswer = regexp.MustCompile(`<AllowedOrigin>([^<]+)</AllowedOrigin>`)

func corsHeld(before probeAnswer) (corsPlan, error) {
	switch {
	case before.code == "200" && len(before.body) > 0:
		found := originInAnswer.FindSubmatch(before.body)
		if found == nil {
			return corsPlan{said: "a configuration naming no origin at all"}, nil
		}
		return corsPlan{body: before.body, origin: string(found[1]), read: true}, nil
	case before.code == "404", bytes.Contains(before.body, []byte("NoSuchCORSConfiguration")):
		body, err := corsBody([]string{probeOrigin})
		if err != nil {
			return corsPlan{}, err
		}
		return corsPlan{body: body, origin: probeOrigin, drop: true, read: true}, nil
	default:
		return corsPlan{said: "the store answered " + before.code + " asked what origins it answers"}, nil
	}
}

func probeCalls(store ExternalStore, root string, cors corsPlan, now time.Time) ([]probeCall, error) {
	once := map[string]string{"If-None-Match": "*"}

	var made []func() (probeCall, error)
	if cors.read {
		sum := md5.Sum(cors.body)
		typed := map[string]string{
			"Content-Type": "application/xml",
			"Content-MD5":  base64.StdEncoding.EncodeToString(sum[:]),
		}
		made = append(made,
			func() (probeCall, error) {
				return store.call("cors-put", http.MethodPut, "", "cors", typed, cors.body, false, now)
			},
			func() (probeCall, error) {
				return store.call("cors-get", http.MethodGet, "", "cors", nil, nil, true, now)
			},
		)
	}
	made = append(made,
		func() (probeCall, error) {
			return store.call("conditional-put", http.MethodPut, root+"conditional", "", once, []byte(probeBody), false, now)
		},
		func() (probeCall, error) {
			return store.call("conditional-again", http.MethodPut, root+"conditional", "", once, []byte(probeBody), false, now)
		},
		func() (probeCall, error) {
			return store.presigned("presigned-put", http.MethodPut, root+"presigned", false, []byte(probeBody), now)
		},
		func() (probeCall, error) {
			return store.presigned("presigned-get", http.MethodGet, root+"presigned", true, nil, now)
		},
		func() (probeCall, error) { return store.postForm("post-policy", root+"posted", now) },
		func() (probeCall, error) {
			return store.call("multipart-create", http.MethodPost, root+"multipart", "uploads", nil, nil, true, now)
		},
	)
	calls := make([]probeCall, 0, len(made))
	for _, make := range made {
		call, err := make()
		if err != nil {
			return nil, err
		}
		calls = append(calls, call)
	}
	return calls, nil
}

func probeCleanup(store ExternalStore, root string, answers map[string]probeAnswer, cors corsPlan, now time.Time) ([]probeCall, error) {
	var calls []probeCall
	if held := answers["multipart-create"]; held.code == "200" {
		if found := uploadIDInAnswer.FindSubmatch(held.body); found != nil {
			call, err := store.call("multipart-abort", http.MethodDelete, root+"multipart",
				"uploadId="+url.QueryEscape(string(found[1])), nil, nil, false, now)
			if err != nil {
				return nil, err
			}
			calls = append(calls, call)
		}
	}
	for _, key := range []string{"conditional", "presigned", "posted"} {
		call, err := store.call("delete-"+key, http.MethodDelete, root+key, "", nil, nil, false, now)
		if err != nil {
			return nil, err
		}
		calls = append(calls, call)
	}
	switch {
	case !cors.read:
		return calls, nil
	case cors.drop:
		call, err := store.call("cors-drop", http.MethodDelete, "", "cors", nil, nil, false, now)
		if err != nil {
			return nil, err
		}
		return append(calls, call), nil
	default:
		sum := md5.Sum(cors.body)
		call, err := store.call("cors-restore", http.MethodPut, "", "cors", map[string]string{
			"Content-Type": "application/xml",
			"Content-MD5":  base64.StdEncoding.EncodeToString(sum[:]),
		}, cors.body, false, now)
		if err != nil {
			return nil, err
		}
		return append(calls, call), nil
	}
}

func lacking(answers map[string]probeAnswer, cors corsPlan) []string {
	var missing []string
	if !answeredWith(answers, "conditional-put", storeWrote...) || !answeredWith(answers, "conditional-again", "412") {
		missing = append(missing, "refuse a second write of a key it already holds (If-None-Match: *), which is how an upload session is claimed exactly once")
	}
	corsWorks := cors.read &&
		answeredWith(answers, "cors-put", storeWrote...) && answeredWith(answers, "cors-get", "200") &&
		strings.Contains(string(answers["cors-get"].body), cors.origin)
	if !corsWorks {
		held := "hold a bucket to the origins it answers (PutBucketCors and GetBucketCors), which is how a browser reaches it"
		if cors.said != "" {
			held += " — " + cors.said + ", and ocel changes no configuration it could not read back first"
		}
		missing = append(missing, held)
	}
	if !answeredWith(answers, "presigned-put", storeWrote...) || !answeredWith(answers, "presigned-get", "200") ||
		string(answers["presigned-get"].body) != probeBody {
		missing = append(missing, "serve a presigned url, which is how every byte an app reads or writes reaches it")
	}
	if !answeredWith(answers, "multipart-create", "200") ||
		!uploadIDInAnswer.Match(answers["multipart-create"].body) {
		missing = append(missing, "open a multipart upload, which is how anything larger than 16 MB is sent")
	}
	return missing
}

func unprobeable(err error) error {
	return providerkit.Refuse(providerkit.CodeInvalid,
		"the store option %q cannot be probed as it is written: %v", "bucket", err)
}

func probeExternalStore(store ExternalStore, root string, now time.Time, run probeRunner) (StoreProbe, error) {
	before, err := store.call("cors-before", http.MethodGet, "", "cors", nil, nil, true, now)
	if err != nil {
		return StoreProbe{}, unprobeable(err)
	}
	said, err := run("read what browsers the store at "+store.Endpoint+" already answers", probeScript([]probeCall{before}))
	if err != nil {
		return StoreProbe{}, err
	}
	cors, err := corsHeld(readProbe(said)["cors-before"])
	if err != nil {
		return StoreProbe{}, unprobeable(err)
	}

	calls, err := probeCalls(store, root, cors, now)
	if err != nil {
		return StoreProbe{}, unprobeable(err)
	}
	said, probed := run("probe the store at "+store.Endpoint, probeScript(calls))
	answers := readProbe(said)

	cleanup, err := probeCleanup(store, root, answers, cors, now)
	if err != nil {
		return StoreProbe{}, errors.Join(probed, unprobeable(err))
	}
	_, swept := run("take the probe of "+store.Endpoint+" back down", probeScript(cleanup))
	if probed != nil || swept != nil {
		return StoreProbe{}, errors.Join(probed, swept)
	}

	if missing := lacking(answers, cors); len(missing) > 0 {
		return StoreProbe{}, providerkit.Refuse(providerkit.CodeInvalid,
			"option %q points this project's objects at bucket %s on %s, and that store cannot:\n\n  - %s\n\n"+
				"Nothing was provisioned. Point the option at a store that serves all of them, or drop it and let the box run a store of its own.",
			"bucket", store.Bucket, store.Endpoint, strings.Join(missing, "\n  - "))
	}
	return StoreProbe{PostPolicies: answeredWith(answers, "post-policy", storeWrote...)}, nil
}

func (h *Host) ProbeExternalStore(ctx context.Context, store ExternalStore) (StoreProbe, error) {
	root, err := probeRoot()
	if err != nil {
		return StoreProbe{}, fmt.Errorf("name a probe of %s: %w", store.Endpoint, err)
	}
	return probeExternalStore(store, root, time.Now().UTC(), func(what, script string) (string, error) {
		return h.ran(ctx, what, script, nil, "")
	})
}
