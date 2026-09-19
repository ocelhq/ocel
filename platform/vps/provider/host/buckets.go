package host

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

// BucketSpec is one bucket inside a box's object store, and how to reach it.
type BucketSpec struct {
	Store    string
	Class    providerkit.Class
	Endpoint string
	Region   string

	AccessKeyID string
	SecretKey   string

	Bucket         string
	AllowedOrigins []string
	Public         bool
}

const (
	multipartExpiry   = 1
	corsMaxAgeSeconds = 3000
)

type corsRule struct {
	AllowedOrigin []string `xml:"AllowedOrigin"`
	AllowedMethod []string `xml:"AllowedMethod"`
	AllowedHeader []string `xml:"AllowedHeader"`
	ExposeHeader  []string `xml:"ExposeHeader"`
	MaxAgeSeconds int      `xml:"MaxAgeSeconds"`
}

type corsConfiguration struct {
	XMLName xml.Name   `xml:"CORSConfiguration"`
	Rules   []corsRule `xml:"CORSRule"`
}

type lifecycleAbort struct {
	DaysAfterInitiation int `xml:"DaysAfterInitiation"`
}

type lifecycleRule struct {
	ID     string         `xml:"ID"`
	Status string         `xml:"Status"`
	Filter struct{}       `xml:"Filter"`
	Abort  lifecycleAbort `xml:"AbortIncompleteMultipartUpload"`
}

type lifecycleConfiguration struct {
	XMLName xml.Name        `xml:"LifecycleConfiguration"`
	Rules   []lifecycleRule `xml:"Rule"`
}

func corsBody(origins []string) ([]byte, error) {
	if len(origins) == 0 {
		origins = []string{}
	}
	return xml.Marshal(corsConfiguration{Rules: []corsRule{{
		AllowedOrigin: origins,
		AllowedMethod: []string{"GET", "HEAD", "PUT", "POST", "DELETE"},
		AllowedHeader: []string{"*"},
		ExposeHeader:  []string{"ETag", "Content-Length", "Content-Type"},
		MaxAgeSeconds: corsMaxAgeSeconds,
	}}})
}

func lifecycleBody() ([]byte, error) {
	return xml.Marshal(lifecycleConfiguration{Rules: []lifecycleRule{{
		ID:     "ocel-abort-incomplete-multipart",
		Status: "Enabled",
		Abort:  lifecycleAbort{DaysAfterInitiation: multipartExpiry},
	}}})
}

func anonymousReadPolicy(bucket string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{map[string]any{
			"Sid":       "OcelAnonymousRead",
			"Effect":    "Allow",
			"Principal": map[string]any{"AWS": []string{"*"}},
			"Action":    []string{"s3:GetObject"},
			"Resource":  []string{"arn:aws:s3:::" + bucket + "/*"},
		}},
	})
}

type storeCall struct {
	what  string
	query string
	body  []byte
	typed string
	md5   bool
	allow []string
}

func (s BucketSpec) calls() ([]storeCall, error) {
	cors, err := corsBody(s.AllowedOrigins)
	if err != nil {
		return nil, err
	}
	lifecycle, err := lifecycleBody()
	if err != nil {
		return nil, err
	}
	calls := []storeCall{
		{what: "create bucket " + s.Bucket, allow: []string{"200", "204", "409"}},
		{what: "hold bucket " + s.Bucket + " to the origins it answers", query: "cors", body: cors, typed: "application/xml", md5: true, allow: []string{"200", "204"}},
		{what: "have bucket " + s.Bucket + " abandon unfinished uploads", query: "lifecycle", body: lifecycle, typed: "application/xml", md5: true, allow: []string{"200", "204", "400", "404", "501"}},
	}
	if s.Public {
		policy, err := anonymousReadPolicy(s.Bucket)
		if err != nil {
			return nil, err
		}
		calls = append(calls, storeCall{
			what:  "open bucket " + s.Bucket + " to anonymous reads",
			query: "policy", body: policy, typed: "application/json", allow: []string{"200", "204"},
		})
	}
	return calls, nil
}

func (s BucketSpec) signed(call storeCall, now time.Time) (*http.Request, error) {
	target := strings.TrimSuffix(s.Endpoint, "/") + "/" + s.Bucket
	if call.query != "" {
		target += "?" + call.query
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	method := http.MethodPut
	req, err := http.NewRequest(method, parsed.String(), strings.NewReader(string(call.body)))
	if err != nil {
		return nil, err
	}
	req.ContentLength = int64(len(call.body))
	if call.typed != "" {
		req.Header.Set("Content-Type", call.typed)
	}
	if call.md5 {
		sum := md5.Sum(call.body)
		req.Header.Set("Content-MD5", base64.StdEncoding.EncodeToString(sum[:]))
	}
	payload := sha256.Sum256(call.body)
	signRequest(req, credential{
		AccessKeyID: s.AccessKeyID,
		SecretKey:   s.SecretKey,
		Region:      s.Region,
	}, hex.EncodeToString(payload[:]), now)
	return req, nil
}

func curlCommand(store string, req *http.Request, call storeCall) string {
	argv := []string{"docker", "exec", "--interactive", store,
		"curl", "--silent", "--show-error", "--output", "/dev/null",
		"--write-out", "%{http_code}", "--request", req.Method}
	for _, name := range sortedHeaderNames(req.Header) {
		argv = append(argv, "--header", name+": "+req.Header.Get(name))
	}
	argv = append(argv, "--data-binary", "@-", req.URL.String())

	fed := "printf '%s' " + quoted(base64.StdEncoding.EncodeToString(call.body)) + " | base64 -d | "
	accepted := make([]string, 0, len(call.allow))
	for _, code := range call.allow {
		accepted = append(accepted, "\""+code+"\")")
	}
	return "set -eu\n" +
		"answered=$(" + fed + words(argv) + ")\n" +
		"case \"$answered\" in\n" +
		strings.Join(accepted, "|") + " ;;\n" +
		"*) printf '%s\\n' " + quoted("the store answered ") + "\"$answered\" >&2; exit 1 ;;\n" +
		"esac"
}

func sortedHeaderNames(header http.Header) []string {
	names := make([]string, 0, len(header))
	for name := range header {
		names = append(names, name)
	}
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return names
}

// ProvisionBucket creates the bucket this resource names inside the box's
// store, holds it to the origins a browser may upload from, has it abandon
// unfinished uploads, and opens it to anonymous reads where the project asked
// for that.
//
// Every request is signed here and replayed by curl inside the store's own
// container: the store answers on the project network alone, so nothing off
// the box can reach it and no port is published to make it reachable.
func (h *Host) ProvisionBucket(ctx context.Context, spec BucketSpec) error {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	calls, err := spec.calls()
	if err != nil {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"bucket %s cannot be described to the store: %v", spec.Bucket, err)
	}
	now := time.Now().UTC()
	for _, call := range calls {
		req, err := spec.signed(call, now)
		if err != nil {
			return fmt.Errorf("sign %s: %w", call.what, err)
		}
		if _, err := h.ran(ctx, call.what, curlCommand(spec.Store, req, call), nil, elevation); err != nil {
			return providerkit.Refuse(providerkit.CodeNotReady,
				"could not %s on %s: %v", call.what, h.named(), err)
		}
	}
	return nil
}

// BucketRef names one bucket inside a box's store, and the store that holds it.
type BucketRef struct {
	Class   providerkit.Class
	Project string
	Store   string
	Bucket  string
}

// RemoveBucket empties the bucket and removes it, and takes the store down
// with the last bucket a project kept in it.
func (h *Host) RemoveBucket(ctx context.Context, ref BucketRef) error {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	if _, err := h.ran(ctx, "take bucket "+ref.Bucket+" and its objects down",
		emptyBucketCommand(ref), nil, elevation); err != nil {
		return err
	}
	return nil
}

func emptyBucketCommand(ref BucketRef) string {
	return "if docker ps --format '{{.Names}}' | grep -qx " + quoted(ref.Store) + "; then\n" +
		"docker exec " + quoted(ref.Store) + " sh -c " +
		quoted("rm -rf /data/"+ref.Bucket) + "\n" +
		"fi"
}
