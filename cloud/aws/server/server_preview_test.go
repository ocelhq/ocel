package server

import (
	"testing"
	"time"

	"github.com/ocelhq/ocel/cloud/aws/bootstrap"
	"github.com/ocelhq/ocel/cloud/aws/deploy"
	providerv1 "github.com/ocelhq/ocel/pkg/proto/provider/v1"
)

func TestPreviewExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name      string
		lifecycle providerv1.Environment_Lifecycle
		want      int64
	}{
		{"ephemeral gets now+ttl", providerv1.Environment_LIFECYCLE_EPHEMERAL, now.Add(previewTTL).Unix()},
		{"persistent has no expiry", providerv1.Environment_LIFECYCLE_PERSISTENT, 0},
		{"unspecified (production) has no expiry", providerv1.Environment_LIFECYCLE_UNSPECIFIED, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := previewExpiry(tc.lifecycle, now); got != tc.want {
				t.Errorf("previewExpiry(%v) = %d, want %d", tc.lifecycle, got, tc.want)
			}
		})
	}
}

func TestStackName(t *testing.T) {
	cases := []struct {
		name string
		env  *providerv1.Environment
		want string
	}{
		{"nil env keeps production", nil, "proj_123-prod"},
		{
			"production class keeps production",
			&providerv1.Environment{Class: providerv1.Environment_CLASS_PRODUCTION},
			"proj_123-prod",
		},
		{
			"preview ephemeral isolates by identity",
			&providerv1.Environment{Class: providerv1.Environment_CLASS_PREVIEW, Lifecycle: providerv1.Environment_LIFECYCLE_EPHEMERAL, Identity: "feature_login_ab12"},
			"proj_123-preview-feature_login_ab12",
		},
		{
			"preview persistent isolates by identity",
			&providerv1.Environment{Class: providerv1.Environment_CLASS_PREVIEW, Lifecycle: providerv1.Environment_LIFECYCLE_PERSISTENT, Identity: "staging"},
			"proj_123-preview-staging",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stackName("proj_123", tc.env); got != tc.want {
				t.Errorf("stackName() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPreflightResponse(t *testing.T) {
	preview := bootstrap.Deployed{Present: true, Class: bootstrap.ClassPreview}
	production := bootstrap.Deployed{Present: true, Class: bootstrap.ClassProduction}
	absent := bootstrap.Deployed{Present: false}

	cases := []struct {
		name          string
		required      providerv1.Environment_Class
		preview, prod bootstrap.Deployed
		wantClass     providerv1.Environment_Class
		wantPresent   bool
	}{
		// Both substrates present: each command gates against its own class,
		// never a spurious mismatch (the bug this scoping fixes).
		{"deploy with both reports production", providerv1.Environment_CLASS_PRODUCTION, preview, production, providerv1.Environment_CLASS_PRODUCTION, true},
		{"preview with both reports preview", providerv1.Environment_CLASS_PREVIEW, preview, production, providerv1.Environment_CLASS_PREVIEW, true},
		// Required substrate present alone.
		{"preview required, preview present", providerv1.Environment_CLASS_PREVIEW, preview, absent, providerv1.Environment_CLASS_PREVIEW, true},
		{"production required, production present", providerv1.Environment_CLASS_PRODUCTION, absent, production, providerv1.Environment_CLASS_PRODUCTION, true},
		// Wrong account: required absent, the other present -> report the other
		// so the caller's guard fires an informative mismatch.
		{"deploy in a preview-only account reports preview", providerv1.Environment_CLASS_PRODUCTION, preview, absent, providerv1.Environment_CLASS_PREVIEW, true},
		{"preview in a production-only account reports production", providerv1.Environment_CLASS_PREVIEW, absent, production, providerv1.Environment_CLASS_PRODUCTION, true},
		// Empty account.
		{"empty account reports absent", providerv1.Environment_CLASS_PREVIEW, absent, absent, providerv1.Environment_CLASS_UNSPECIFIED, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := preflightResponse(tc.required, tc.preview, tc.prod)
			if got.GetInfraClass() != tc.wantClass || got.GetInfrastructurePresent() != tc.wantPresent {
				t.Errorf("preflightResponse() = {class=%v present=%v}, want {class=%v present=%v}",
					got.GetInfraClass(), got.GetInfrastructurePresent(), tc.wantClass, tc.wantPresent)
			}
		})
	}
}

func TestToPreviewEnvironments(t *testing.T) {
	stacks := []deploy.PreviewStack{
		{Identity: "feature_login_ab12", Lifecycle: providerv1.Environment_LIFECYCLE_EPHEMERAL, Label: "pr-7", CreatedAt: 100, ExpiresAt: 200},
		{Identity: "staging", Lifecycle: providerv1.Environment_LIFECYCLE_PERSISTENT},
	}

	got := toPreviewEnvironments(stacks)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].GetIdentity() != "feature_login_ab12" || got[0].GetLifecycle() != providerv1.Environment_LIFECYCLE_EPHEMERAL ||
		got[0].GetLabel() != "pr-7" || got[0].GetCreatedAt() != 100 || got[0].GetExpiresAt() != 200 {
		t.Errorf("first env = %+v, want the ephemeral entry mapped through", got[0])
	}
	if got[1].GetIdentity() != "staging" || got[1].GetLifecycle() != providerv1.Environment_LIFECYCLE_PERSISTENT {
		t.Errorf("second env = %+v, want the persistent entry mapped through", got[1])
	}
}

func TestToPreviewEnvironments_Empty(t *testing.T) {
	if got := toPreviewEnvironments(nil); len(got) != 0 {
		t.Errorf("toPreviewEnvironments(nil) = %+v, want empty", got)
	}
}
