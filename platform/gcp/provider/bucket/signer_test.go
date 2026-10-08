package bucket

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func signingWith(t *testing.T) (signer, func([]byte) ([]byte, error), *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	sign := func(payload []byte) ([]byte, error) {
		digest := sha256.Sum256(payload)
		return rsa.SignPKCS1v15(nil, key, crypto.SHA256, digest[:])
	}
	store := Store{
		Account:  func(context.Context) (string, error) { return appAccount, nil },
		SignBlob: func(_ context.Context, _ string, payload []byte) ([]byte, error) { return sign(payload) },
	}
	return signer{store: store, host: "storage.googleapis.com"}, sign, key
}

func signedAt(t *testing.T, signedURL string) (time.Time, time.Duration) {
	t.Helper()
	query := signedQuery(t, signedURL)
	at, err := time.Parse(signedDateFormat, query.Get("X-Goog-Date"))
	if err != nil {
		t.Fatalf("the signed url names date %q: %v", query.Get("X-Goog-Date"), err)
	}
	seconds, err := strconv.Atoi(query.Get("X-Goog-Expires"))
	if err != nil {
		t.Fatal(err)
	}
	return at, time.Duration(seconds) * time.Second
}

func TestASignedURLIsTheOneGooglesOwnLibrarySignsForTheSameRequest(t *testing.T) {
	ours, sign, _ := signingWith(t)
	for name, tc := range map[string]struct {
		method  string
		headers []string
		ctype   string
		query   url.Values
		mine    func(signer, time.Duration) (string, error)
	}{
		"a read downloaded under a name": {
			method: "GET",
			query:  url.Values{dispositionQuery: {`attachment; filename="q3 report.pdf"`}},
			mine: func(s signer, lifetime time.Duration) (string, error) {
				signed, err := s.PresignGetObject(context.Background(), &s3.GetObjectInput{
					Bucket: aws.String(uploads), Key: aws.String("reports/q3 report.pdf"),
					ResponseContentDisposition: aws.String(`attachment; filename="q3 report.pdf"`),
				}, func(o *s3.PresignOptions) { o.Expires = lifetime })
				if err != nil {
					return "", err
				}
				return signed.URL, nil
			},
		},
		"a create-only write with metadata": {
			method:  "PUT",
			ctype:   "application/pdf",
			headers: []string{"x-goog-if-generation-match:0", "x-goog-meta-owner:ada"},
			mine: func(s signer, lifetime time.Duration) (string, error) {
				signed, err := s.PresignPutObject(context.Background(), &s3.PutObjectInput{
					Bucket: aws.String(uploads), Key: aws.String("reports/q3 report.pdf"), ContentType: aws.String("application/pdf"),
					IfNoneMatch: aws.String("*"), Metadata: map[string]string{"owner": "ada"},
				}, func(o *s3.PresignOptions) { o.Expires = lifetime })
				if err != nil {
					return "", err
				}
				return signed.URL, nil
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			theirs, err := storage.SignedURL(uploads, "reports/q3 report.pdf", &storage.SignedURLOptions{
				GoogleAccessID: appAccount, SignBytes: sign, Scheme: storage.SigningSchemeV4, Method: tc.method,
				Expires: time.Now().Add(time.Hour), Headers: tc.headers, ContentType: tc.ctype, QueryParameters: tc.query,
				Hostname: "storage.googleapis.com", Style: storage.PathStyle(),
			})
			if err != nil {
				t.Fatalf("storage.SignedURL() = %v", err)
			}
			at, lifetime := signedAt(t, theirs)
			pinned := ours
			pinned.now = func() time.Time { return at }
			mine, err := tc.mine(pinned, lifetime)
			if err != nil {
				t.Fatalf("sign = %v", err)
			}
			if mine != theirs {
				t.Errorf("signed\n%s\nwant what Google's library signs for the same request\n%s", mine, theirs)
			}
		})
	}
}

func conditionsOf(t *testing.T, policy string) ([]string, string) {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(policy)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Conditions []json.RawMessage `json:"conditions"`
		Expiration string            `json:"expiration"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	conditions := make([]string, 0, len(document.Conditions))
	for _, condition := range document.Conditions {
		conditions = append(conditions, string(condition))
	}
	slices.Sort(conditions)
	return conditions, document.Expiration
}

func TestABrowserFormHoldsTheConditionsGooglesOwnLibraryWritesAndASignatureOverThem(t *testing.T) {
	ours, sign, key := signingWith(t)
	theirs, err := storage.GenerateSignedPostPolicyV4(avatars, "ada.png", &storage.PostPolicyV4Options{
		GoogleAccessID: appAccount, SignRawBytes: sign, Expires: time.Now().Add(time.Hour), Hostname: "storage.googleapis.com",
		Fields:     &storage.PolicyV4Fields{ContentType: "image/png", Metadata: map[string]string{"x-goog-meta-owner": "ada"}},
		Conditions: []storage.PostPolicyV4Condition{storage.ConditionContentLengthRange(4, 4)},
	})
	if err != nil {
		t.Fatalf("storage.GenerateSignedPostPolicyV4() = %v", err)
	}
	at, err := time.Parse(signedDateFormat, theirs.Fields["x-goog-date"])
	if err != nil {
		t.Fatal(err)
	}
	wanted, expiration := conditionsOf(t, theirs.Fields["policy"])
	expires, err := time.Parse(time.RFC3339, expiration)
	if err != nil {
		t.Fatal(err)
	}
	pinned := ours
	pinned.now = func() time.Time { return at }

	mine, err := pinned.PresignPostObject(context.Background(), &s3.PutObjectInput{Bucket: aws.String(avatars), Key: aws.String("ada.png")},
		func(o *s3.PresignPostOptions) {
			o.Expires = expires.Sub(at)
			o.Conditions = []any{
				map[string]string{"Content-Type": "image/png"},
				[]any{"content-length-range", int64(4), int64(4)},
				map[string]string{"x-goog-meta-owner": "ada"},
			}
		})
	if err != nil {
		t.Fatalf("PresignPostObject() = %v", err)
	}
	if mine.URL != theirs.URL {
		t.Errorf("the form posts to %s, want %s", mine.URL, theirs.URL)
	}
	got, gotExpiration := conditionsOf(t, mine.Values["policy"])
	if until, err := time.Parse(time.RFC3339, gotExpiration); err != nil || !slices.Equal(got, wanted) || !until.Equal(expires) {
		t.Errorf("the policy holds %v until %s, want %v until %s", got, gotExpiration, wanted, expiration)
	}
	for name, value := range theirs.Fields {
		if name == "policy" || name == "x-goog-signature" {
			continue
		}
		if form := formNames[name]; form != "" {
			name = form
		}
		if mine.Values[name] != value {
			t.Errorf("the form's %s is %q, want %q", name, mine.Values[name], value)
		}
	}
	signature, err := hex.DecodeString(mine.Values["x-goog-signature"])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(mine.Values["policy"]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Errorf("the form's signature does not verify over its policy: %v", err)
	}
	if strings.Contains(mine.Values["policy"], "Content-Type") {
		t.Error("the policy names Content-Type, and Google's library names every field lowercase")
	}
}
