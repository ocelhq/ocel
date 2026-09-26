package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const adminPath = "/rustfs/admin/v3/"

const (
	StoreSecretMin = 8
	StoreSecretMax = 40
)

func CheckStoreSecret(secret string) error {
	if len(secret) < StoreSecretMin || len(secret) > StoreSecretMax {
		return refusal.Refuse(refusal.CodeInvalid,
			"a store account secret is %d characters; the store takes %d to %d",
			len(secret), StoreSecretMin, StoreSecretMax)
	}
	return nil
}

type StoreAccount struct {
	Store    string
	Class    edge.Class
	Endpoint string
	Region   string

	RootKeyID  string
	RootSecret string

	AccessKeyID string
	SecretKey   string
	Buckets     []string
	Sessions    string
}

type adminCall struct {
	what   string
	method string
	action string
	query  string
	body   []byte
	allow  []string
}

var (
	bucketActions = []string{"s3:ListBucket", "s3:GetBucketLocation", "s3:ListBucketMultipartUploads"}
	objectActions = []string{"s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"}
)

func accountPolicy(buckets []string, sessions string) ([]byte, error) {
	named := make([]string, 0, len(buckets))
	objects := make([]string, 0, len(buckets)+1)
	for _, bucket := range buckets {
		named = append(named, "arn:aws:s3:::"+bucket)
		objects = append(objects, "arn:aws:s3:::"+bucket+"/*")
	}
	if sessions != "" {
		objects = append(objects, "arn:aws:s3:::"+strings.TrimSuffix(sessions, "/")+"/*")
	}
	var statements []any
	if len(named) > 0 {
		statements = append(statements, map[string]any{
			"Effect": "Allow", "Action": bucketActions, "Resource": named,
		})
	}
	if len(objects) > 0 {
		statements = append(statements, map[string]any{
			"Effect": "Allow", "Action": objectActions, "Resource": objects,
		})
	}
	return json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": statements})
}

func (a StoreAccount) calls() ([]adminCall, error) {
	if err := CheckStoreSecret(a.SecretKey); err != nil {
		return nil, err
	}
	policy, err := accountPolicy(a.Buckets, a.Sessions)
	if err != nil {
		return nil, err
	}
	var document any
	if err := json.Unmarshal(policy, &document); err != nil {
		return nil, err
	}
	added, err := json.Marshal(map[string]any{
		"targetUser": a.RootKeyID,
		"accessKey":  a.AccessKeyID,
		"secretKey":  a.SecretKey,
		"policy":     document,
	})
	if err != nil {
		return nil, err
	}
	updated, err := json.Marshal(map[string]any{
		"newSecretKey": a.SecretKey,
		"newPolicy":    document,
	})
	if err != nil {
		return nil, err
	}
	return []adminCall{
		{
			what:   "create store account " + a.AccessKeyID,
			method: http.MethodPut, action: "add-service-account",
			body: added, allow: []string{"200", "400"},
		},
		{
			what:   "set the policy of store account " + a.AccessKeyID,
			method: http.MethodPost, action: "update-service-account",
			query: "accessKey=" + url.QueryEscape(a.AccessKeyID),
			body:  updated, allow: []string{"200", "204"},
		},
	}, nil
}

func (a StoreAccount) signed(call adminCall, now time.Time) (*http.Request, error) {
	target := strings.TrimSuffix(a.Endpoint, "/") + adminPath + call.action
	if call.query != "" {
		target += "?" + call.query
	}
	req, err := http.NewRequest(call.method, target, strings.NewReader(string(call.body)))
	if err != nil {
		return nil, err
	}
	req.ContentLength = int64(len(call.body))
	req.Header.Set("Content-Type", "application/json")
	payload := sha256.Sum256(call.body)
	signRequest(req, credential{
		AccessKeyID: a.RootKeyID, SecretKey: a.RootSecret, Region: a.Region,
	}, hex.EncodeToString(payload[:]), now)
	return req, nil
}

func accountScript(a StoreAccount, call adminCall, now time.Time) (string, error) {
	req, err := a.signed(call, now)
	if err != nil {
		return "", err
	}
	return curlCommand(a.Store, req, storeCall{
		what: call.what, body: call.body, allow: call.allow,
	}), nil
}

func StoreAccountKey(env, app string) string {
	sum := sha256.Sum256([]byte(env + "/" + app))
	return "ocel" + hex.EncodeToString(sum[:8])
}

func (a StoreAccount) revoking() adminCall {
	return adminCall{
		what:   "delete store account " + a.AccessKeyID,
		method: http.MethodDelete, action: "delete-service-account",
		query: "accessKey=" + url.QueryEscape(a.AccessKeyID),
		allow: []string{"200", "204", "404"},
	}
}

func (h *Host) RevokeStoreAccount(ctx context.Context, account StoreAccount) error {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	call := account.revoking()
	script, err := accountScript(account, call, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("sign %s: %w", call.what, err)
	}
	if _, err := h.ran(ctx, call.what, script, fedBody(call.body), elevation); err != nil {
		return refusal.Refuse(refusal.CodeNotReady,
			"could not %s on %s: %v", call.what, h.named(), err)
	}
	return nil
}

func (h *Host) GrantStoreAccount(ctx context.Context, account StoreAccount) error {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	calls, err := account.calls()
	if err != nil {
		return refusal.Refuse(refusal.CodeInvalid,
			"cannot encode store account %s: %v", account.AccessKeyID, err)
	}
	now := time.Now().UTC()
	for _, call := range calls {
		script, err := accountScript(account, call, now)
		if err != nil {
			return fmt.Errorf("sign %s: %w", call.what, err)
		}
		if _, err := h.ran(ctx, call.what, script, fedBody(call.body), elevation); err != nil {
			return refusal.Refuse(refusal.CodeNotReady,
				"could not %s on %s: %v", call.what, h.named(), err)
		}
	}
	return nil
}
