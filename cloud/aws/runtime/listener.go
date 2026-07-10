package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Event is the minimal shape of the S3 ObjectCreated notification the listener
// consumes. It mirrors the field subset of the AWS Lambda S3 event JSON the
// handler needs (bucket name + object key), so a real Lambda runtime can
// unmarshal an invocation straight into it and a test can hand-build one.
type S3Event struct {
	Records []S3EventRecord `json:"Records"`
}

type S3EventRecord struct {
	S3 S3Entity `json:"s3"`
}

type S3Entity struct {
	Bucket S3Bucket `json:"bucket"`
	Object S3Object `json:"object"`
}

type S3Bucket struct {
	Name string `json:"name"`
}

type S3Object struct {
	// Key arrives URL-encoded in real S3 events (spaces as '+', etc.); the
	// handler decodes it before matching against the session's stored key.
	Key string `json:"key"`
}

// objectTagger reads an object's tag set. S3 event notifications do not carry
// tags, so the listener reads the SigV4-bound sessionId tag off the landed
// object itself. Narrowed for testability; the s3.Client satisfies it.
type objectTagger interface {
	GetObjectTagging(context.Context, *s3.GetObjectTaggingInput, ...func(*s3.Options)) (*s3.GetObjectTaggingOutput, error)
}

// httpDoer is the HTTP client the listener posts op=callback through. *http.Client
// satisfies it; tests substitute a recorder.
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Listener is the production completion detector: on an S3 ObjectCreated event
// it reads the object's signed sessionId tag, loads the session, performs the
// atomic idempotent pending -> succeeded transition, and — only when it is the
// transition that fired — signs the completion with the per-session secret and
// POSTs op=callback to the session's callback_base_url. The callback target is
// validated against the deploy-known allowedOrigins so a spoofed
// callback_base_url can never redirect the signed callback off-origin.
type Listener struct {
	store          *sessionStore
	tagger         objectTagger
	http           httpDoer
	allowedOrigins []string
}

// ListenerConfig wires a Listener to its concrete clients and deploy-known
// origin allowlist.
type ListenerConfig struct {
	DDB            ddbAPI
	Tagger         objectTagger
	HTTP           httpDoer
	Table          string
	AllowedOrigins []string
}

// NewListener builds a Listener, defaulting the HTTP client to the shared
// default when the caller supplies none.
func NewListener(cfg ListenerConfig) *Listener {
	h := cfg.HTTP
	if h == nil {
		h = http.DefaultClient
	}
	return &Listener{
		store:          &sessionStore{client: cfg.DDB, table: cfg.Table},
		tagger:         cfg.Tagger,
		http:           h,
		allowedOrigins: cfg.AllowedOrigins,
	}
}

// signedCompletion is the op=callback request body: {sessionId, signature,
// file}. It is byte-identical to the dev detector's payload (cli devserver
// runtimeShim) and to what the SDK route's callback op parses, so the prod and
// dev completion wires agree.
type signedCompletion struct {
	SessionID string        `json:"sessionId"`
	Signature string        `json:"signature"`
	File      completedFile `json:"file"`
}

type completedFile struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	MimeType string `json:"mimeType"`
}

// Handle processes an S3 ObjectCreated event, completing every record it
// carries. A record whose session or file cannot be resolved, or whose callback
// target is not allowlisted, is skipped without failing the batch — an
// unresolvable object must not wedge the notification.
func (l *Listener) Handle(ctx context.Context, event S3Event) error {
	for _, rec := range event.Records {
		if err := l.handleRecord(ctx, rec); err != nil {
			return err
		}
	}
	return nil
}

func (l *Listener) handleRecord(ctx context.Context, rec S3EventRecord) error {
	bucket := rec.S3.Bucket.Name
	key, err := url.QueryUnescape(rec.S3.Object.Key)
	if err != nil {
		key = rec.S3.Object.Key
	}

	sessionID, err := l.sessionIDForObject(ctx, bucket, key)
	if err != nil {
		return err
	}
	if sessionID == "" {
		// Object carries no session tag — not ours to complete.
		return nil
	}

	sess, err := l.store.get(ctx, sessionID)
	if err != nil {
		if errors.Is(err, errSessionNotFound) {
			return nil
		}
		return err
	}

	idx := indexOfFile(sess.Files, key)
	if idx < 0 {
		// The tag resolved a session, but no file in it matches this key.
		return nil
	}

	transitioned, err := l.store.markSucceeded(ctx, sessionID, idx)
	if err != nil {
		return err
	}
	if !transitioned {
		// Already non-pending: a duplicate delivery. Do NOT call the route.
		return nil
	}

	return l.postCallback(ctx, sess, sess.Files[idx])
}

// sessionIDForObject reads the object's tag set and returns the sessionId tag
// value, or "" when the object carries no such tag.
func (l *Listener) sessionIDForObject(ctx context.Context, bucket, key string) (string, error) {
	out, err := l.tagger.GetObjectTagging(ctx, &s3.GetObjectTaggingInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return "", fmt.Errorf("read tags for %s/%s: %w", bucket, key, err)
	}
	for _, t := range out.TagSet {
		if aws.ToString(t.Key) == sessionTagKey {
			return aws.ToString(t.Value), nil
		}
	}
	return "", nil
}

// postCallback signs the completed file with the per-session secret and POSTs
// op=callback to the session's callback_base_url, but only after validating that
// target against the deploy-known allowedOrigins. The secret never leaves this
// process; the route re-verifies the signature via VerifyUploadSignature.
func (l *Listener) postCallback(ctx context.Context, sess session, f sessionFile) error {
	if !originAllowed(sess.CallbackBaseURL, l.allowedOrigins) {
		// A spoofed callback_base_url whose origin isn't declared is rejected:
		// the signed callback is never delivered off an allowlisted origin.
		return nil
	}

	signed := SignedFile{Key: f.Key, Name: f.Name, Size: f.Size, MimeType: f.MimeType}
	body, err := json.Marshal(signedCompletion{
		SessionID: sess.SessionID,
		Signature: signUpload(sess.Secret, sess.SessionID, signed),
		File:      completedFile{Key: f.Key, Name: f.Name, Size: f.Size, MimeType: f.MimeType},
	})
	if err != nil {
		return fmt.Errorf("encode callback: %w", err)
	}

	target, err := callbackURL(sess.CallbackBaseURL)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build callback request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := l.http.Do(req)
	if err != nil {
		return fmt.Errorf("post callback: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("callback returned status %d", resp.StatusCode)
	}
	return nil
}

// callbackURL appends ?op=callback to the session's callback base URL, the op
// the SDK route multiplexes completion callbacks on.
func callbackURL(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse callback base url %q: %w", base, err)
	}
	q := u.Query()
	q.Set("op", "callback")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// originAllowed reports whether the origin (scheme://host[:port]) of rawURL is
// present in allowed. An unparseable or opaque URL is never allowed. This is the
// prod callback-trust check: the same allowedOrigins list drives bucket CORS.
func originAllowed(rawURL string, allowed []string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	origin := u.Scheme + "://" + u.Host
	for _, a := range allowed {
		if a == origin {
			return true
		}
	}
	return false
}

// indexOfFile returns the index of the first file whose key matches, or -1.
func indexOfFile(files []sessionFile, key string) int {
	for i, f := range files {
		if f.Key == key {
			return i
		}
	}
	return -1
}
