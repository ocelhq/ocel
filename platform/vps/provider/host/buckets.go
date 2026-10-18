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
	"strconv"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type BucketSpec struct {
	Store    string
	Tier     environment.Tier
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

func newCORSRule(origins []string) corsRule {
	return corsRule{
		AllowedOrigin: origins,
		AllowedMethod: []string{"GET", "HEAD", "PUT", "POST", "DELETE"},
		AllowedHeader: []string{"*"},
		ExposeHeader:  []string{"ETag", "Content-Length", "Content-Type"},
		MaxAgeSeconds: corsMaxAgeSeconds,
	}
}

func corsBody(origins []string) ([]byte, error) {
	if len(origins) == 0 {
		return nil, nil
	}
	return xml.Marshal(corsConfiguration{Rules: []corsRule{newCORSRule(origins)}})
}

func newLifecycleRule() lifecycleRule {
	return lifecycleRule{
		ID:     "ocel-abort-incomplete-multipart",
		Status: "Enabled",
		Abort:  lifecycleAbort{DaysAfterInitiation: multipartExpiry},
	}
}

func lifecycleBody() ([]byte, error) {
	return xml.Marshal(lifecycleConfiguration{Rules: []lifecycleRule{newLifecycleRule()}})
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
		cors, err := s.corsCall()
		if err != nil {
			return nil, err
		}
		calls = append(calls, cors)
	}
	calls = append(calls, storeCall{
		name: lifecycleCall, what: "set the lifecycle of bucket " + s.Bucket,
		method: http.MethodPut,
		query:  "lifecycle", body: lifecycle, typed: "application/xml", md5: true, allow: []string{"200", "204", "400", "404", "501"},
	})
	switch {
	case s.Internal:
		return calls, nil
	case !s.Public:
		return append(calls, storeCall{
			name: "policy", what: "close bucket " + s.Bucket + " to anonymous reads",
			method: http.MethodDelete, query: "policy", allow: []string{"200", "204", "404"},
		}), nil
	}
	policy, err := anonymousReadPolicy(s.Bucket)
	if err != nil {
		return nil, err
	}
	return append(calls, storeCall{
		name: "policy", what: "open bucket " + s.Bucket + " to anonymous reads",
		method: http.MethodPut,
		query:  "policy", body: policy, typed: "application/json", allow: []string{"200", "204"},
	}), nil
}

func (s BucketSpec) corsCall() (storeCall, error) {
	cors, err := corsBody(s.AllowedOrigins)
	if err != nil {
		return storeCall{}, err
	}
	if cors == nil {
		return storeCall{
			name: "cors", what: "clear CORS on bucket " + s.Bucket,
			method: http.MethodDelete, query: "cors", allow: []string{"200", "204", "404"},
		}, nil
	}
	return storeCall{
		name: "cors", what: "set CORS on bucket " + s.Bucket,
		method: http.MethodPut,
		query:  "cors", body: cors, typed: "application/xml", md5: true, allow: []string{"200", "204"},
	}, nil
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
	argv := append(storeExec(store), "--show-error", "--output", "/dev/null",
		"--write-out", "%{http_code}", "--request", req.Method)
	for _, name := range sortedHeaderNames(req.Header) {
		argv = append(argv, "--header", name+": "+req.Header.Get(name))
	}
	argv = append(argv, "--data-binary", "@-", req.URL.String())

	accepted := make([]string, 0, len(call.allow))
	for _, code := range call.allow {
		accepted = append(accepted, "\""+code+"\"")
	}
	return "set -eu\n" +
		"answered=$(base64 -d | " + words(argv) + ") || { printf '%s\\n' " + quoted("the store "+store+" gave no answer") + " >&2; exit " + strconv.Itoa(storeBusyExit) + "; }\n" +
		"case \"$answered\" in\n" +
		strings.Join(accepted, "|") + ") ;;\n" +
		"000|429|5??) printf '%s\\n' " + quoted("the store answered ") + "\"$answered\" >&2; exit " + strconv.Itoa(storeBusyExit) + " ;;\n" +
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

type BucketState struct {
	ExpiresUploads bool
}

func (h *Host) ProvisionBucket(ctx context.Context, spec BucketSpec) (BucketState, error) {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return BucketState{}, err
	}
	calls, err := spec.calls()
	if err != nil {
		return BucketState{}, refusal.Refuse(refusal.CodeInvalid,
			"cannot encode bucket %s: %v", spec.Bucket, err)
	}
	now := time.Now().UTC()
	var current BucketState
	for _, call := range calls {
		if call.name == lifecycleCall {
			taken, err := droveStore(spec, []storeCall{call}, now, func(what, script string) (string, error) {
				return h.ran(ctx, what, script, nil, elevation)
			})
			if err != nil {
				return BucketState{}, refusal.Refuse(refusal.CodeNotReady,
					"could not %s on %s: %v", call.what, h.named(), err)
			}
			current.ExpiresUploads = slices.Contains(storeTook, taken[call.name].code)
			continue
		}
		req, err := spec.signed(call, now)
		if err != nil {
			return BucketState{}, fmt.Errorf("sign %s: %w", call.what, err)
		}
		if err := h.calledStore(ctx, call.what, curlCommand(spec.Store, req, call), call.body, elevation); err != nil {
			return BucketState{}, err
		}
	}
	return current, nil
}

func (h *Host) ApplyOrigins(ctx context.Context, spec BucketSpec) error {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	call, err := spec.corsCall()
	if err != nil {
		return refusal.Refuse(refusal.CodeInvalid,
			"bucket %s cannot be described to the store: %v", spec.Bucket, err)
	}
	req, err := spec.signed(call, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("sign %s: %w", call.what, err)
	}
	return h.calledStore(ctx, call.what, curlCommand(spec.Store, req, call), call.body, elevation)
}

const locationCall = "location"

func (s BucketSpec) readingCalls() []storeCall {
	calls := []storeCall{{
		name: locationCall, what: "read whether bucket " + s.Bucket + " exists",
		method: http.MethodGet, query: "location", allow: []string{"200", "404"},
	}}
	if !s.Internal {
		calls = append(calls,
			storeCall{
				name: "cors", what: "read the CORS of bucket " + s.Bucket,
				method: http.MethodGet, query: "cors", capture: true, allow: []string{"200", "404"},
			},
			storeCall{
				name: "policy", what: "read the policy of bucket " + s.Bucket,
				method: http.MethodGet, query: "policy", capture: true, allow: []string{"200", "404"},
			})
	}
	return append(calls, storeCall{
		name: lifecycleCall, what: "read the lifecycle of bucket " + s.Bucket,
		method: http.MethodGet, query: "lifecycle", capture: true, allow: []string{"200", "400", "404", "501"},
	})
}

func (h *Host) PlanBucket(ctx context.Context, spec BucketSpec) (provider.ChangeAction, string, error) {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return "", "", err
	}
	answers, err := droveStore(spec, spec.readingCalls(), time.Now().UTC(), func(what, script string) (string, error) {
		return h.ran(ctx, what, script, nil, elevation)
	})
	if err != nil {
		return "", "", refusal.Refuse(refusal.CodeNotReady,
			"could not read bucket %s on %s: %v", spec.Bucket, h.named(), err)
	}
	return spec.planChange(answers)
}

func (s BucketSpec) planChange(answers map[string]storeAnswer) (provider.ChangeAction, string, error) {
	location, spoke := answers[locationCall]
	switch {
	case !spoke:
		return provider.ActionUpdate, "its store " + s.Store + " is not running to read it from, so its CORS, policy and lifecycle are set again", nil
	case location.code == "404":
		return provider.ActionCreate, "", nil
	}
	var reasons []string
	if !s.Internal {
		cors, err := s.planCORS(answers["cors"])
		if err != nil {
			return "", "", err
		}
		reasons = append(reasons, cors, s.planPolicy(answers["policy"]))
	}
	lifecycle, err := planLifecycle(answers[lifecycleCall])
	if err != nil {
		return "", "", err
	}
	reasons = slices.DeleteFunc(append(reasons, lifecycle), func(reason string) bool { return reason == "" })
	if len(reasons) == 0 {
		return provider.ActionKeep, provider.ReasonCurrent, nil
	}
	return provider.ActionUpdate, strings.Join(reasons, "; "), nil
}

func (s BucketSpec) planCORS(answer storeAnswer) (string, error) {
	if answer.code == "" {
		return "its CORS could not be read, so it is set again", nil
	}
	var held corsConfiguration
	if answer.code == "200" && len(strings.TrimSpace(string(answer.body))) > 0 {
		if err := xml.Unmarshal(answer.body, &held); err != nil {
			return "", refusal.Refuse(refusal.CodeNotReady,
				"the store answered the CORS of bucket %s with what is no CORS configuration: %v", s.Bucket, err)
		}
	}
	var heldOrigins []string
	for _, rule := range held.Rules {
		heldOrigins = append(heldOrigins, rule.AllowedOrigin...)
	}
	want := newCORSRule(s.AllowedOrigins)
	switch {
	case len(held.Rules) == 0 && len(s.AllowedOrigins) == 0:
		return "", nil
	case len(s.AllowedOrigins) == 0:
		return "it stops answering cross-origin requests from " + fmt.Sprint(heldOrigins), nil
	case len(held.Rules) == 0:
		return "it starts answering cross-origin requests from " + fmt.Sprint(s.AllowedOrigins), nil
	case len(held.Rules) > 1 || !isSameCORSSettings(held.Rules[0], want):
		return "its CORS rules are replaced by one answering " + fmt.Sprint(s.AllowedOrigins), nil
	case !isSameSet(held.Rules[0].AllowedOrigin, want.AllowedOrigin):
		return "its allowed origins change from " + fmt.Sprint(heldOrigins) + " to " + fmt.Sprint(s.AllowedOrigins), nil
	default:
		return "", nil
	}
}

func isSameSet(a, b []string) bool {
	return slices.Equal(slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b)))
}

func isSameCORSSettings(held, want corsRule) bool {
	return held.MaxAgeSeconds == want.MaxAgeSeconds &&
		isSameSet(held.AllowedMethod, want.AllowedMethod) &&
		isSameSet(held.AllowedHeader, want.AllowedHeader) &&
		isSameSet(held.ExposeHeader, want.ExposeHeader)
}

func (s BucketSpec) planPolicy(answer storeAnswer) string {
	if answer.code == "" {
		return "its policy could not be read, so it is set again"
	}
	held := answer.code == "200" && len(strings.TrimSpace(string(answer.body))) > 0
	statements := readPolicyStatements(answer.body)
	anonymous := held && slices.ContainsFunc(statements, grantsAnyone)
	switch {
	case s.Public && held && isAnonymousRead(statements, s.Bucket):
		return ""
	case s.Public && !held:
		return "it becomes readable by anyone"
	case s.Public:
		return "its policy is replaced by one that lets anyone read it"
	case anonymous:
		return "it stops being readable by anyone"
	case held:
		return "its policy is removed"
	default:
		return ""
	}
}

func readPolicyStatements(body []byte) []map[string]any {
	var policy struct {
		Statement json.RawMessage `json:"Statement"`
	}
	if err := json.Unmarshal(body, &policy); err != nil {
		return nil
	}
	var statements []map[string]any
	if err := json.Unmarshal(policy.Statement, &statements); err == nil {
		return statements
	}
	var statement map[string]any
	if err := json.Unmarshal(policy.Statement, &statement); err != nil {
		return nil
	}
	return []map[string]any{statement}
}

func grantsAnyone(statement map[string]any) bool {
	if statement["Effect"] != "Allow" {
		return false
	}
	switch principal := statement["Principal"].(type) {
	case string:
		return principal == "*"
	case map[string]any:
		return slices.Equal(readPolicyValues(principal["AWS"]), []string{"*"})
	default:
		return false
	}
}

func isAnonymousRead(statements []map[string]any, bucket string) bool {
	if len(statements) != 1 {
		return false
	}
	statement := statements[0]
	for key := range statement {
		if !slices.Contains([]string{"Sid", "Effect", "Principal", "Action", "Resource"}, key) {
			return false
		}
	}
	return grantsAnyone(statement) &&
		slices.Equal(readPolicyValues(statement["Action"]), []string{"s3:GetObject"}) &&
		slices.Equal(readPolicyValues(statement["Resource"]), []string{"arn:aws:s3:::" + bucket + "/*"})
}

func readPolicyValues(value any) []string {
	switch value := value.(type) {
	case string:
		return []string{value}
	case []any:
		values := make([]string, 0, len(value))
		for _, each := range value {
			text, isText := each.(string)
			if !isText {
				return nil
			}
			values = append(values, text)
		}
		return values
	default:
		return nil
	}
}

type heldLifecycleRule struct {
	Status string `xml:"Status"`
	Prefix string `xml:"Prefix"`
	Filter struct {
		Prefix string    `xml:"Prefix"`
		Tag    *struct{} `xml:"Tag"`
		And    *struct{} `xml:"And"`
	} `xml:"Filter"`
	Abort      *lifecycleAbort `xml:"AbortIncompleteMultipartUpload"`
	Expiration *struct{}       `xml:"Expiration"`
	Transition *struct{}       `xml:"Transition"`
}

type heldLifecycle struct {
	Rules []heldLifecycleRule `xml:"Rule"`
}

func planLifecycle(answer storeAnswer) (string, error) {
	const expiring = "unfinished uploads to it start expiring after a day"
	switch answer.code {
	case "400", "501":
		return "", nil
	case "200":
	default:
		return expiring, nil
	}
	var held heldLifecycle
	if err := xml.Unmarshal(answer.body, &held); err != nil {
		return "", refusal.Refuse(refusal.CodeNotReady,
			"the store answered a bucket's lifecycle with what is no lifecycle configuration: %v", err)
	}
	want := newLifecycleRule()
	if len(held.Rules) == 1 {
		rule := held.Rules[0]
		if rule.Status == want.Status && rule.Prefix == "" && rule.Filter.Prefix == "" &&
			rule.Filter.Tag == nil && rule.Filter.And == nil && rule.Expiration == nil && rule.Transition == nil &&
			rule.Abort != nil && *rule.Abort == want.Abort {
			return "", nil
		}
	}
	if len(held.Rules) == 0 {
		return expiring, nil
	}
	return "its lifecycle rules are replaced by one expiring unfinished uploads after a day", nil
}

type BucketRef struct {
	Tier    environment.Tier
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
		Tier:        r.Tier,
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
