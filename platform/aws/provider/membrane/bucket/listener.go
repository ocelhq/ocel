package bucket

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
	Key string `json:"key"`
}

type objectTagger interface {
	GetObjectTagging(context.Context, *s3.GetObjectTaggingInput, ...func(*s3.Options)) (*s3.GetObjectTaggingOutput, error)
}

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Listener struct {
	store          *sessionStore
	tagger         objectTagger
	http           httpDoer
	allowedOrigins []string
}

type ListenerConfig struct {
	DDB            ddbAPI
	Tagger         objectTagger
	HTTP           httpDoer
	Table          string
	AllowedOrigins []string
}

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
		return nil
	}

	transitioned, err := l.store.markSucceeded(ctx, sessionID, idx)
	if err != nil {
		return err
	}
	if !transitioned {
		return nil
	}

	return l.postCallback(ctx, sess, sess.Files[idx])
}

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

func (l *Listener) postCallback(ctx context.Context, sess session, f sessionFile) error {
	if !originAllowed(sess.CallbackBaseURL, l.allowedOrigins) {
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

func indexOfFile(files []sessionFile, key string) int {
	for i, f := range files {
		if f.Key == key {
			return i
		}
	}
	return -1
}
