package bucket

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	s3store "github.com/ocelhq/ocel/platform/s3"
)

const (
	metadataHeader      = "x-goog-meta-"
	generationHeader    = "x-goog-if-generation-match"
	lengthRangeHeader   = "x-goog-content-length-range"
	contentLengthRange  = "content-length-range"
	dispositionQuery    = "response-content-disposition"
	signingAlgorithm    = "GOOG4-RSA-SHA256"
	unsignedPayload     = "UNSIGNED-PAYLOAD"
	signedDateFormat    = "20060102T150405Z"
	scopeDateFormat     = "20060102"
	scopeSuffix         = "/auto/storage/goog4_request"
	defaultSignLifetime = time.Hour
)

type signer struct {
	store    Store
	host     string
	insecure bool
	now      func() time.Time
}

var _ s3store.PresignAPI = signer{}

type signedRequest struct {
	method string
	header http.Header
	query  url.Values
}

type credential struct {
	account string
	at      time.Time
}

func (c credential) date() string { return c.at.Format(signedDateFormat) }

func (c credential) scope() string { return c.at.Format(scopeDateFormat) + scopeSuffix }

func (c credential) String() string { return c.account + "/" + c.scope() }

func (s signer) credential(ctx context.Context) (credential, error) {
	account, err := s.store.Account(ctx)
	if err != nil {
		return credential{}, fmt.Errorf("read the account this app signs as: %w", err)
	}
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	return credential{account: account, at: now().UTC()}, nil
}

func (s signer) scheme() string {
	if s.insecure {
		return "http"
	}
	return "https"
}

func escapePath(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		segments[i] = strings.ReplaceAll(url.QueryEscape(segment), "+", "%20")
	}
	return strings.Join(segments, "/")
}

func escapeQuery(query url.Values) string {
	return strings.ReplaceAll(query.Encode(), "+", "%20")
}

func (s signer) signBytes(ctx context.Context, signed credential, payload []byte) (string, error) {
	signature, err := s.store.SignBlob(ctx, signed.account, payload)
	if err != nil {
		return "", fmt.Errorf("sign as %s: %w", signed.account, err)
	}
	return hex.EncodeToString(signature), nil
}

func (s signer) sign(ctx context.Context, bucket, key string, lifetime time.Duration, request signedRequest) (*v4.PresignedHTTPRequest, error) {
	signed, err := s.credential(ctx)
	if err != nil {
		return nil, err
	}
	lines := map[string]string{"host": s.host}
	for name, values := range request.header {
		lines[strings.ToLower(name)] = strings.Join(strings.Fields(values[0]), " ")
	}
	names := slices.Sorted(maps.Keys(lines))
	headers := make([]string, 0, len(names))
	for _, name := range names {
		headers = append(headers, name+":"+lines[name])
	}
	query := url.Values{}
	for name, values := range request.query {
		query[name] = slices.Clone(values)
	}
	query.Set("X-Goog-Algorithm", signingAlgorithm)
	query.Set("X-Goog-Credential", signed.String())
	query.Set("X-Goog-Date", signed.date())
	query.Set("X-Goog-Expires", strconv.Itoa(int(lifetimeOr(lifetime).Seconds())))
	query.Set("X-Goog-SignedHeaders", strings.Join(names, ";"))
	at := &url.URL{Scheme: s.scheme(), Host: s.host, Path: "/" + bucket + "/" + key}
	at.RawPath = "/" + escapePath(bucket+"/"+key)
	canonical := strings.Join([]string{
		request.method,
		at.RawPath,
		escapeQuery(query),
		strings.Join(headers, "\n") + "\n",
		strings.Join(names, ";"),
		unsignedPayload,
	}, "\n")
	digest := sha256.Sum256([]byte(canonical))
	signature, err := s.signBytes(ctx, signed, []byte(strings.Join([]string{signingAlgorithm, signed.date(), signed.scope(), hex.EncodeToString(digest[:])}, "\n")))
	if err != nil {
		return nil, err
	}
	query.Set("X-Goog-Signature", signature)
	at.RawQuery = query.Encode()
	return &v4.PresignedHTTPRequest{URL: at.String(), Method: request.method, SignedHeader: request.header}, nil
}

func lifetimeOr(lifetime time.Duration) time.Duration {
	if lifetime <= 0 {
		return defaultSignLifetime
	}
	return lifetime
}

func presignLifetime(opts []func(*s3.PresignOptions)) time.Duration {
	var options s3.PresignOptions
	for _, apply := range opts {
		apply(&options)
	}
	return options.Expires
}

func (s signer) PresignGetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	query := url.Values{}
	if disposition := aws.ToString(in.ResponseContentDisposition); disposition != "" {
		query.Set(dispositionQuery, disposition)
	}
	return s.sign(ctx, aws.ToString(in.Bucket), aws.ToString(in.Key), presignLifetime(opts),
		signedRequest{method: http.MethodGet, header: http.Header{}, query: query})
}

func (s signer) PresignPutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	header := http.Header{}
	set := func(name, value string) {
		if value != "" {
			header.Set(name, value)
		}
	}
	set("Content-Type", aws.ToString(in.ContentType))
	set("Cache-Control", aws.ToString(in.CacheControl))
	set("Content-Disposition", aws.ToString(in.ContentDisposition))
	for name, value := range in.Metadata {
		header.Set(metadataHeader+name, value)
	}
	switch {
	case aws.ToString(in.IfNoneMatch) == "*":
		header.Set(generationHeader, "0")
	case aws.ToString(in.IfMatch) != "":
		generation, err := generationOf(aws.ToString(in.IfMatch))
		if err != nil {
			return nil, err
		}
		header.Set(generationHeader, formatGeneration(generation))
	}
	if length := aws.ToInt64(in.ContentLength); length > 0 {
		header.Set(lengthRangeHeader, strconv.FormatInt(length, 10)+","+strconv.FormatInt(length, 10))
	}
	return s.sign(ctx, aws.ToString(in.Bucket), aws.ToString(in.Key), presignLifetime(opts),
		signedRequest{method: http.MethodPut, header: header})
}

func (s signer) PresignUploadPart(ctx context.Context, in *s3.UploadPartInput, opts ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	query := url.Values{
		"partNumber": {strconv.Itoa(int(aws.ToInt32(in.PartNumber)))},
		"uploadId":   {aws.ToString(in.UploadId)},
	}
	return s.sign(ctx, aws.ToString(in.Bucket), aws.ToString(in.Key), presignLifetime(opts),
		signedRequest{method: http.MethodPut, header: http.Header{}, query: query})
}

type postPolicy struct {
	Conditions []any  `json:"conditions"`
	Expiration string `json:"expiration"`
}

func (s signer) PresignPostObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.PresignPostOptions)) (*s3.PresignedPostRequest, error) {
	var options s3.PresignPostOptions
	for _, apply := range opts {
		apply(&options)
	}
	fields, conditions, err := formOf(options.Conditions)
	if err != nil {
		return nil, err
	}
	signed, err := s.credential(ctx)
	if err != nil {
		return nil, err
	}
	bucket, key := aws.ToString(in.Bucket), aws.ToString(in.Key)
	fields["key"] = key
	fields["x-goog-date"] = signed.date()
	fields["x-goog-credential"] = signed.String()
	fields["x-goog-algorithm"] = signingAlgorithm
	for _, name := range []string{"bucket", "key", "x-goog-date", "x-goog-credential", "x-goog-algorithm"} {
		value := bucket
		if name != "bucket" {
			value = fields[name]
		}
		conditions = append(conditions, map[string]string{name: value})
	}
	document, err := json.Marshal(postPolicy{Conditions: conditions, Expiration: signed.at.Add(lifetimeOr(options.Expires)).Format(time.RFC3339)})
	if err != nil {
		return nil, err
	}
	policy := base64.StdEncoding.EncodeToString(document)
	signature, err := s.signBytes(ctx, signed, []byte(policy))
	if err != nil {
		return nil, err
	}
	fields["policy"] = policy
	fields["x-goog-signature"] = signature
	at := &url.URL{Scheme: s.scheme(), Host: s.host, Path: "/" + bucket + "/"}
	return &s3.PresignedPostRequest{URL: at.String(), Values: fields}, nil
}

var formNames = map[string]string{
	"content-type":        "Content-Type",
	"cache-control":       "Cache-Control",
	"content-disposition": "Content-Disposition",
}

func formOf(conditions []any) (map[string]string, []any, error) {
	fields := map[string]string{}
	var policy []any
	for _, condition := range conditions {
		switch held := condition.(type) {
		case map[string]string:
			for name, value := range held {
				lower := strings.ToLower(name)
				form, known := formNames[lower]
				if !known && !strings.HasPrefix(lower, metadataHeader) {
					return nil, nil, fmt.Errorf("a browser upload's policy names the field %q, which a Cloud Storage form does not take", name)
				}
				if !known {
					form = lower
				}
				fields[form] = value
				policy = append(policy, map[string]string{lower: value})
			}
		case []any:
			if len(held) != 3 || held[0] != contentLengthRange {
				return nil, nil, fmt.Errorf("a browser upload's policy holds a condition %v Cloud Storage has no form of", condition)
			}
			policy = append(policy, held)
		default:
			return nil, nil, fmt.Errorf("a browser upload's policy holds a condition %v Cloud Storage has no form of", condition)
		}
	}
	return fields, policy, nil
}
