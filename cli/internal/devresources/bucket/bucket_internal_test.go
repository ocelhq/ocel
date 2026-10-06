package bucket

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	s3store "github.com/ocelhq/ocel/platform/s3"
)

func TestTheDevStoreSignsAPostPolicyThatBoundsASignedUploadsSize(t *testing.T) {
	store := s3store.Store{Endpoint: "http://127.0.0.1:9000", Region: "local", AccessKeyID: "ocel", SecretAccessKey: "dev-secret-key", PathStyle: true}
	service := serve(store, map[string][]string{"dev-uploads-1a2b3c4d": nil})

	signed, err := service.Sign(context.Background(), &bucketv1.SignRequest{
		Bucket:      "dev-uploads-1a2b3c4d",
		Key:         "reports/q4.pdf",
		Operation:   bucketv1.SignedOperation_SIGNED_OPERATION_POST_UPLOAD,
		Audience:    bucketv1.SignedAudience_SIGNED_AUDIENCE_EXTERNAL,
		Constraints: &bucketv1.SignConstraints{MaxSize: 1024, ContentType: "application/pdf"},
	})
	if err != nil {
		t.Fatalf("Sign = %v, want a POST form target", err)
	}
	target := signed.GetTarget()
	if target.GetMethod() != "POST" {
		t.Fatalf("target method = %q, want POST", target.GetMethod())
	}
	raw, err := base64.StdEncoding.DecodeString(target.GetFields()["policy"])
	if err != nil {
		t.Fatalf("the policy field is not base64: %v", err)
	}
	var policy struct {
		Conditions []json.RawMessage `json:"conditions"`
	}
	if err := json.Unmarshal(raw, &policy); err != nil {
		t.Fatalf("the policy is not JSON: %v", err)
	}
	found := false
	for _, condition := range policy.Conditions {
		var parts []any
		if json.Unmarshal(condition, &parts) != nil {
			continue
		}
		again, _ := json.Marshal(parts)
		if string(again) == `["content-length-range",0,1024]` {
			found = true
		}
	}
	if !found {
		t.Errorf("policy conditions = %s, want content-length-range 0..1024", raw)
	}
	if got := target.GetFields()["Content-Type"]; got != "application/pdf" {
		t.Errorf("Content-Type field = %q, want application/pdf", got)
	}
}
