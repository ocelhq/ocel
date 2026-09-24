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
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

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
	Internal       bool
}

const (
	multipartExpiry   = 1
	corsMaxAgeSeconds = 3000
	lifecycleCall     = "lifecycle"
)

var storeTook = []string{"200", "204"}

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
		return nil, nil
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
	name    string
	what    string
	method  string
	key     string
	query   string
	body    []byte
	typed   string
	md5     bool
	capture bool
	allow   []string
}

func (s BucketSpec) calls() ([]storeCall, error) {
	lifecycle, err := lifecycleBody()
	if err != nil {
		return nil, err
	}
	calls := []storeCall{
		{
			name: "create", what: "create bucket " + s.Bucket, method: http.MethodPut,
			allow: []string{"200", "204", "409"},
		},
	}
	if !s.Internal {
		cors, err := corsBody(s.AllowedOrigins)
		if err != nil {
			return nil, err
		}
		if cors != nil {
			calls = append(calls, storeCall{
				name: "cors", what: "set CORS on bucket " + s.Bucket,
				method: http.MethodPut,
				query:  "cors", body: cors, typed: "application/xml", md5: true, allow: []string{"200", "204"},
			})
		}
	}
	calls = append(calls, storeCall{
		name: lifecycleCall, what: "set the lifecycle of bucket " + s.Bucket,
		method: http.MethodPut,
		query:  "lifecycle", body: lifecycle, typed: "application/xml", md5: true, allow: []string{"200", "204", "400", "404", "501"},
	})
	if s.Public {
		policy, err := anonymousReadPolicy(s.Bucket)
		if err != nil {
			return nil, err
		}
		calls = append(calls, storeCall{
			name: "policy", what: "open bucket " + s.Bucket + " to anonymous reads",
			method: http.MethodPut,
			query:  "policy", body: policy, typed: "application/json", allow: []string{"200", "204"},
		})
	}
	return calls, nil
}

func (s BucketSpec) signed(call storeCall, now time.Time) (*http.Request, error) {
	target := strings.TrimSuffix(s.Endpoint, "/") + "/" + s.Bucket
	if call.key != "" {
		target += "/" + call.key
	}
	if call.query != "" {
		target += "?" + call.query
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(call.method, parsed.String(), strings.NewReader(string(call.body)))
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

func fedBody(body []byte) io.Reader {
	return strings.NewReader(base64.StdEncoding.EncodeToString(body))
}

func curlCommand(store string, req *http.Request, call storeCall) string {
	argv := []string{"docker", "exec", "--interactive", store,
		"curl", "--silent", "--show-error", "--output", "/dev/null",
		"--write-out", "%{http_code}", "--request", req.Method}
	for _, name := range sortedHeaderNames(req.Header) {
		argv = append(argv, "--header", name+": "+req.Header.Get(name))
	}
	argv = append(argv, "--data-binary", "@-", req.URL.String())

	accepted := make([]string, 0, len(call.allow))
	for _, code := range call.allow {
		accepted = append(accepted, "\""+code+"\"")
	}
	return "set -eu\n" +
		"answered=$(base64 -d | " + words(argv) + ")\n" +
		"case \"$answered\" in\n" +
		strings.Join(accepted, "|") + ") ;;\n" +
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

type BucketStanding struct {
	ExpiresUploads bool
}

func (h *Host) ProvisionBucket(ctx context.Context, spec BucketSpec) (BucketStanding, error) {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return BucketStanding{}, err
	}
	calls, err := spec.calls()
	if err != nil {
		return BucketStanding{}, providerkit.Refuse(providerkit.CodeInvalid,
			"cannot encode bucket %s: %v", spec.Bucket, err)
	}
	now := time.Now().UTC()
	var standing BucketStanding
	for _, call := range calls {
		if call.name == lifecycleCall {
			taken, err := droveStore(spec, []storeCall{call}, now, func(what, script string) (string, error) {
				return h.ran(ctx, what, script, nil, elevation)
			})
			if err != nil {
				return BucketStanding{}, providerkit.Refuse(providerkit.CodeNotReady,
					"could not %s on %s: %v", call.what, h.named(), err)
			}
			standing.ExpiresUploads = slices.Contains(storeTook, taken[call.name].code)
			continue
		}
		req, err := spec.signed(call, now)
		if err != nil {
			return BucketStanding{}, fmt.Errorf("sign %s: %w", call.what, err)
		}
		if _, err := h.ran(ctx, call.what, curlCommand(spec.Store, req, call), fedBody(call.body), elevation); err != nil {
			return BucketStanding{}, providerkit.Refuse(providerkit.CodeNotReady,
				"could not %s on %s: %v", call.what, h.named(), err)
		}
	}
	return standing, nil
}

type BucketRef struct {
	Class   providerkit.Class
	Project string
	Store   string
	Bucket  string

	Endpoint    string
	Region      string
	AccessKeyID string
	SecretKey   string
}

func (r BucketRef) spec() BucketSpec {
	return BucketSpec{
		Store:       r.Store,
		Class:       r.Class,
		Endpoint:    r.Endpoint,
		Region:      r.Region,
		AccessKeyID: r.AccessKeyID,
		SecretKey:   r.SecretKey,
		Bucket:      r.Bucket,
	}
}

func (h *Host) RemoveBucket(ctx context.Context, ref BucketRef) error {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	return removedBucket(ref.spec(), func() time.Time { return time.Now().UTC() }, func(what, script string) (string, error) {
		return h.ran(ctx, what, script, nil, elevation)
	})
}
