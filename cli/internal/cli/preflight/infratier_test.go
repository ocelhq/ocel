package preflight

import (
	"strings"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

func TestCheckTier_Matches(t *testing.T) {
	cases := []struct {
		infra, required environmentv1.Tier
	}{
		{environmentv1.Tier_TIER_PREVIEW, environmentv1.Tier_TIER_PREVIEW},
		{environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_PRODUCTION},
	}
	for _, c := range cases {
		if err := checkTier(c.infra, c.required); err != nil {
			t.Errorf("checkTier(%v, %v) = %v, want nil", c.infra, c.required, err)
		}
	}
}

func TestCheckTier_Mismatches(t *testing.T) {
	cases := []struct {
		infra, required environmentv1.Tier
	}{
		{environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_PREVIEW},
		{environmentv1.Tier_TIER_PREVIEW, environmentv1.Tier_TIER_PRODUCTION},
		{environmentv1.Tier_TIER_UNSPECIFIED, environmentv1.Tier_TIER_PREVIEW},
		{environmentv1.Tier_TIER_UNSPECIFIED, environmentv1.Tier_TIER_PRODUCTION},
	}
	for _, c := range cases {
		err := checkTier(c.infra, c.required)
		if err == nil {
			t.Errorf("checkTier(%v, %v) = nil, want error", c.infra, c.required)
			continue
		}
		if !strings.Contains(err.Error(), "infrastructure") {
			t.Errorf("checkTier(%v, %v) error names no infrastructure: %q", c.infra, c.required, err)
		}
	}
}

func TestCheckTier_ErrorNamesTheInfraAndTheBootstrap(t *testing.T) {
	err := checkTier(environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_PREVIEW)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "preview infrastructure") {
		t.Errorf("error should name preview infrastructure, got %q", msg)
	}
	if !strings.Contains(msg, "ocel bootstrap preview") {
		t.Errorf("error should tell the user how to fix it, got %q", msg)
	}
	if strings.Contains(msg, "ocel preview can only") {
		t.Errorf("error names a command the caller may not have run, got %q", msg)
	}

	err = checkTier(environmentv1.Tier_TIER_PREVIEW, environmentv1.Tier_TIER_PRODUCTION)
	if err == nil {
		t.Fatal("expected error")
	}
	msg = err.Error()
	if !strings.Contains(msg, "production infrastructure") {
		t.Errorf("error should name production infrastructure, got %q", msg)
	}
	if !strings.Contains(msg, "ocel bootstrap production") {
		t.Errorf("error should tell the user how to fix it, got %q", msg)
	}
	if strings.Contains(msg, "ocel deploy can only") {
		t.Errorf("error names a command the caller may not have run, got %q", msg)
	}
}
