package host

import (
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
)

const storePage = 1000

type storeAnswer struct {
	code string
	body []byte
}

type storeShell func(what, script string) (string, error)

func readAnswers(said string) map[string]storeAnswer {
	answers := map[string]storeAnswer{}
	for line := range strings.Lines(said) {
		name, value, cut := strings.Cut(strings.TrimSpace(line), "=")
		if !cut {
			continue
		}
		if call, is := strings.CutSuffix(name, ".body"); is {
			decoded, err := base64.StdEncoding.DecodeString(value)
			if err != nil {
				continue
			}
			answer := answers[call]
			answer.body = decoded
			answers[call] = answer
			continue
		}
		answer := answers[name]
		answer.code = value
		answers[name] = answer
	}
	return answers
}

type storedUpload struct {
	Key      string `xml:"Key"`
	UploadID string `xml:"UploadId"`
}

type uploadListing struct {
	XMLName xml.Name       `xml:"ListMultipartUploadsResult"`
	Uploads []storedUpload `xml:"Upload"`
}

type storedObject struct {
	Key string `xml:"Key"`
}

type objectListing struct {
	XMLName  xml.Name       `xml:"ListBucketResult"`
	Contents []storedObject `xml:"Contents"`
}

type deletedObject struct {
	XMLName xml.Name `xml:"Object"`
	Key     string   `xml:"Key"`
}

type deletion struct {
	XMLName xml.Name        `xml:"Delete"`
	Quiet   bool            `xml:"Quiet"`
	Objects []deletedObject `xml:"Object"`
}

func storeScript(store string, runs []*http.Request, calls []storeCall) string {
	written := strings.Builder{}
	written.WriteString("set -u\n" +
		"docker ps --format '{{.Names}}' | grep -qx " + quoted(store) + " || exit 0\n" +
		"tmp=$(mktemp -d)\n" +
		"trap 'rm -rf \"$tmp\"' EXIT\n")
	for i, req := range runs {
		call := calls[i]
		codeFile := "\"$tmp/" + strconv.Itoa(i) + "\""
		argv := append([]string{"docker", "exec", "--interactive", store}, storeCurl...)
		argv = append(argv, "--output", "-", "--write-out", "%{stderr}%{http_code}",
			"--request", req.Method)
		for _, name := range sortedHeaderNames(req.Header) {
			argv = append(argv, "--header", name+": "+req.Header.Get(name))
		}
		if len(call.body) > 0 {
			argv = append(argv, "--data-binary", "@-")
		}
		argv = append(argv, req.URL.String())

		fed := ""
		if len(call.body) > 0 {
			fed = "printf '%s' " + quoted(base64.StdEncoding.EncodeToString(call.body)) + " | base64 -d | "
		}
		written.WriteString("said=$(" + fed + words(argv) + " 2>" + codeFile + " | base64 | tr -d '\\n')\n")
		written.WriteString("printf '%s=%s\\n' " + quoted(call.name) + " \"$(cat " + codeFile + ")\"\n")
		if call.capture {
			written.WriteString("printf '%s.body=%s\\n' " + quoted(call.name) + " \"$said\"\n")
		}
	}
	return written.String()
}

func droveStore(spec BucketSpec, calls []storeCall, now time.Time, run storeShell) (map[string]storeAnswer, error) {
	runs := make([]*http.Request, 0, len(calls))
	what := make([]string, 0, len(calls))
	for _, call := range calls {
		req, err := spec.signed(call, now)
		if err != nil {
			return nil, fmt.Errorf("sign %s: %w", call.what, err)
		}
		runs = append(runs, req)
		what = append(what, call.what)
	}
	said, err := run(strings.Join(what, ", "), storeScript(spec.Store, runs, calls))
	if err != nil {
		return nil, err
	}
	answers := readAnswers(said)
	for _, call := range calls {
		answer, spoke := answers[call.name]
		if !spoke {
			continue
		}
		if answer.code == "000" {
			return nil, refusal.Refuse(refusal.CodeNotReady,
				"the store %s gave no answer asked to %s", spec.Store, call.what)
		}
		if !slices.Contains(call.allow, answer.code) {
			return nil, refusal.Refuse(refusal.CodeNotReady,
				"the store answered %s asked to %s", answer.code, call.what)
		}
	}
	return answers, nil
}

func listingCalls() []storeCall {
	page := strconv.Itoa(storePage)
	return []storeCall{
		{
			name: "uploads", what: "list unfinished uploads",
			method: http.MethodGet, query: "uploads&max-uploads=" + page,
			capture: true, allow: []string{"200", "404"},
		},
		{
			name: "objects", what: "list objects",
			method: http.MethodGet, query: "list-type=2&max-keys=" + page,
			capture: true, allow: []string{"200", "404"},
		},
	}
}

func uploadsIn(answer storeAnswer) ([]storedUpload, error) {
	if answer.code != "200" || len(answer.body) == 0 {
		return nil, nil
	}
	var listed uploadListing
	if err := xml.Unmarshal(answer.body, &listed); err != nil {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"unreadable upload listing from the store: %v", err)
	}
	return listed.Uploads, nil
}

func objectsIn(answer storeAnswer) ([]string, error) {
	if answer.code != "200" || len(answer.body) == 0 {
		return nil, nil
	}
	var listed objectListing
	if err := xml.Unmarshal(answer.body, &listed); err != nil {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"unreadable object listing from the store: %v", err)
	}
	keys := make([]string, 0, len(listed.Contents))
	for _, object := range listed.Contents {
		keys = append(keys, object.Key)
	}
	return keys, nil
}

func abortCalls(uploads []storedUpload) []storeCall {
	calls := make([]storeCall, 0, len(uploads))
	for i, upload := range uploads {
		calls = append(calls, storeCall{
			name: "abort" + strconv.Itoa(i), what: "abort the upload of " + upload.Key,
			method: http.MethodDelete, key: upload.Key,
			query: "uploadId=" + url.QueryEscape(upload.UploadID),
			allow: []string{"200", "204", "404"},
		})
	}
	return calls
}

func deleteCall(keys []string) (storeCall, error) {
	objects := make([]deletedObject, 0, len(keys))
	for _, key := range keys {
		objects = append(objects, deletedObject{Key: key})
	}
	body, err := xml.Marshal(deletion{Quiet: true, Objects: objects})
	if err != nil {
		return storeCall{}, err
	}
	return storeCall{
		name: "delete", what: "delete " + strconv.Itoa(len(keys)) + " objects",
		method: http.MethodPost, query: "delete", body: body,
		typed: "application/xml", md5: true, allow: []string{"200"},
	}, nil
}

func contentSignature(keys []string, uploads []storedUpload) string {
	signature := fmt.Sprint(len(keys), " ", len(uploads))
	if len(keys) > 0 {
		signature += " " + keys[0]
	}
	if len(uploads) > 0 {
		signature += " " + uploads[0].UploadID
	}
	return signature
}

func removedBucket(spec BucketSpec, clock func() time.Time, run storeShell) error {
	for previous := ""; ; {
		listed, err := droveStore(spec, listingCalls(), clock(), run)
		if err != nil {
			return err
		}
		uploads, err := uploadsIn(listed["uploads"])
		if err != nil {
			return err
		}
		keys, err := objectsIn(listed["objects"])
		if err != nil {
			return err
		}
		if len(uploads) == 0 && len(keys) == 0 {
			break
		}
		signature := contentSignature(keys, uploads)
		if signature == previous {
			return refusal.Refuse(refusal.CodeNotReady,
				"bucket %s still contains %d objects and %d unfinished uploads after deletion",
				spec.Bucket, len(keys), len(uploads))
		}
		previous = signature
		calls := abortCalls(uploads)
		if len(keys) > 0 {
			call, err := deleteCall(keys)
			if err != nil {
				return refusal.Refuse(refusal.CodeInvalid,
					"cannot encode the deletion for bucket %s: %v", spec.Bucket, err)
			}
			calls = append(calls, call)
		}
		if _, err := droveStore(spec, calls, clock(), run); err != nil {
			return err
		}
	}
	if _, err := droveStore(spec, []storeCall{{
		name: "remove", what: "remove bucket " + spec.Bucket,
		method: http.MethodDelete, allow: []string{"200", "204", "404"},
	}}, clock(), run); err != nil {
		return err
	}
	return nil
}
