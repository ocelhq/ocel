package originsecret_test

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/originsecret"
)

var mintedAt = time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)

func TestANewSecretIs32RandomBytesStampedWithTheSecondItWasMinted(t *testing.T) {
	t.Parallel()

	first, err := originsecret.New(mintedAt.Add(500 * time.Millisecond).In(time.FixedZone("CEST", 2*60*60)))
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if _, err := hex.DecodeString(first.Current); err != nil || len(first.Current) != 64 {
		t.Errorf("current = %q, want 32 random bytes the front can send as a header", first.Current)
	}
	if !first.CreatedAt.Equal(mintedAt) || first.CreatedAt.Location() != time.UTC {
		t.Errorf("created at = %v, want %v: the record keeps whole UTC seconds", first.CreatedAt, mintedAt)
	}
	if first.Rotating() {
		t.Errorf("a new secret = %+v, want no predecessor", first)
	}
	second, err := originsecret.New(mintedAt)
	if err != nil {
		t.Fatal(err)
	}
	if second.Current == first.Current {
		t.Error("two new secrets are equal")
	}
}

func TestASecretParsesFromTheRecordItMarshalsTo(t *testing.T) {
	t.Parallel()

	rotatedAt := mintedAt.Add(originsecret.MaxAge)
	written := originsecret.Secret{Current: "s2", CreatedAt: rotatedAt, Previous: "s1", RotatedAt: rotatedAt}
	raw, err := json.Marshal(written)
	if err != nil {
		t.Fatal(err)
	}
	read, err := originsecret.Parse(raw)
	if err != nil {
		t.Fatalf("Parse(%s) = %v", raw, err)
	}
	if read != written {
		t.Errorf("Parse(%s) = %+v, want %+v", raw, read, written)
	}

	single, _ := json.Marshal(originsecret.Secret{Current: "s1", CreatedAt: mintedAt})
	if strings.Contains(string(single), "previous") || strings.Contains(string(single), "rotatedAt") {
		t.Errorf("a secret with no predecessor marshals to %s, want neither a previous secret nor a rotation recorded", single)
	}
}

func TestParsingRefusesAnythingButARecordNamingACurrentSecret(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"5e884898da28047151d0e56f8dc6292773603d0d", `{"createdAt":"2026-01-10T12:00:00Z"}`, ""} {
		if read, err := originsecret.Parse([]byte(raw)); err == nil {
			t.Errorf("Parse(%q) = %+v, want it refused", raw, read)
		}
	}
}

func TestASecretYoungerThanTheMaximumAgeIsKept(t *testing.T) {
	t.Parallel()

	young := originsecret.Secret{Current: "s1", CreatedAt: mintedAt}
	renewed, renewal, err := young.Renew(mintedAt.Add(originsecret.MaxAge - time.Second))
	if err != nil {
		t.Fatalf("Renew() = %v", err)
	}
	if renewal != (originsecret.Renewal{}) || renewed != young || renewal.Changed() {
		t.Errorf("Renew() = %+v, %+v, want the secret kept as it is", renewed, renewal)
	}
}

func TestASecretAtTheMaximumAgeIsRotatedKeepingTheOneItReplaces(t *testing.T) {
	t.Parallel()

	rotatedAt := mintedAt.Add(originsecret.MaxAge)
	renewed, renewal, err := originsecret.Secret{Current: "s1", CreatedAt: mintedAt}.Renew(rotatedAt)
	if err != nil {
		t.Fatalf("Renew() = %v", err)
	}
	if !renewal.Rotated || renewal.Retired || !renewal.Changed() {
		t.Errorf("renewal = %+v, want a rotation", renewal)
	}
	if renewed.Current == "s1" || len(renewed.Current) != 64 {
		t.Errorf("current = %q, want a fresh secret", renewed.Current)
	}
	if renewed.Previous != "s1" || !renewed.RotatedAt.Equal(rotatedAt) || !renewed.CreatedAt.Equal(rotatedAt) {
		t.Errorf("renewed = %+v, want s1 kept as the predecessor from %v", renewed, rotatedAt)
	}
}

func TestThePredecessorIsRetiredOnceItsGraceHasRun(t *testing.T) {
	t.Parallel()

	rotatedAt := mintedAt.Add(originsecret.MaxAge)
	rotating := originsecret.Secret{Current: "s2", CreatedAt: rotatedAt, Previous: "s1", RotatedAt: rotatedAt}

	renewed, renewal, err := rotating.Renew(rotatedAt.Add(originsecret.Grace))
	if err != nil {
		t.Fatalf("Renew() = %v", err)
	}
	if !renewal.Retired || renewal.Rotated {
		t.Errorf("renewal = %+v, want the predecessor retired and nothing minted", renewal)
	}
	if renewed != (originsecret.Secret{Current: "s2", CreatedAt: rotatedAt}) {
		t.Errorf("renewed = %+v, want s2 alone", renewed)
	}

	kept, renewal, err := rotating.Renew(rotatedAt.Add(originsecret.Grace - time.Hour))
	if err != nil || renewal.Changed() || kept != rotating {
		t.Errorf("Renew() within the grace = %+v, %+v, %v, want the rotation left to run", kept, renewal, err)
	}
}

func TestARotationWhosePredecessorsGraceHasRunRetiresItAndRotatesAtOnce(t *testing.T) {
	t.Parallel()

	rotating := originsecret.Secret{Current: "s2", CreatedAt: mintedAt, Previous: "s1", RotatedAt: mintedAt}

	renewed, renewal, err := rotating.Renew(mintedAt.Add(originsecret.MaxAge))
	if err != nil {
		t.Fatalf("Renew() = %v", err)
	}
	if !renewal.Retired || !renewal.Rotated {
		t.Errorf("renewal = %+v, want s1 retired and s2 succeeded", renewal)
	}
	if renewed.Previous != "s2" || renewed.Current == "s2" {
		t.Errorf("renewed = %+v, want s2 as the predecessor of a fresh secret", renewed)
	}
}

func TestAPlanSaysWhatABootstrapDoesToTheSecret(t *testing.T) {
	t.Parallel()

	rotatedAt := mintedAt.Add(originsecret.MaxAge)
	for _, tc := range []struct {
		name     string
		existing originsecret.Secret
		now      time.Time
		action   provider.ChangeAction
		reason   string
	}{
		{"absent is created", originsecret.Secret{}, mintedAt, provider.ActionCreate, ""},
		{"young is kept", originsecret.Secret{Current: "s1", CreatedAt: mintedAt}, mintedAt.Add(time.Hour), provider.ActionKeep, provider.ReasonCurrent},
		{"old is rotated", originsecret.Secret{Current: "s1", CreatedAt: mintedAt}, rotatedAt, provider.ActionUpdate,
			"it is older than 90 days and is rotated; the one it replaces stays accepted for 7 days"},
		{"in grace is kept", originsecret.Secret{Current: "s2", CreatedAt: rotatedAt, Previous: "s1", RotatedAt: rotatedAt}, rotatedAt.Add(time.Hour), provider.ActionKeep, provider.ReasonCurrent},
		{"past grace retires", originsecret.Secret{Current: "s2", CreatedAt: rotatedAt, Previous: "s1", RotatedAt: rotatedAt}, rotatedAt.Add(originsecret.Grace), provider.ActionUpdate,
			"the secret it replaced 7 days ago is retired; a release not re-deployed since stops answering its front"},
		{"past grace and old retires and rotates", originsecret.Secret{Current: "s2", CreatedAt: rotatedAt, Previous: "s1", RotatedAt: rotatedAt}, rotatedAt.Add(originsecret.MaxAge), provider.ActionUpdate,
			"the secret it replaced 90 days ago is retired, and it is older than 90 days and is rotated in turn; a release not re-deployed since the earlier rotation stops answering its front, and the one this replaces stays accepted for 7 days"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			action, reason := tc.existing.PlanChange(tc.now)
			if action != tc.action {
				t.Errorf("PlanChange() = %q, want %q", action, tc.action)
			}
			if tc.reason != "" && reason != tc.reason {
				t.Errorf("PlanChange() reason = %q, want %q", reason, tc.reason)
			}
			if action == provider.ActionCreate && reason == "" {
				t.Error("a create names no reason, so the plan cannot say what the secret is for")
			}
		})
	}
}

func TestAStaleNoticeSaysHowOldTheSecretIsAndWhatToRunWithoutTheSecretItself(t *testing.T) {
	t.Parallel()

	aging := originsecret.Secret{Current: "s1", CreatedAt: mintedAt}

	if notice := originsecret.FormatStaleNotice(aging, mintedAt.Add(originsecret.MaxAge-time.Second), environment.TierProduction); notice != "" {
		t.Errorf("notice = %q, want none for a secret within its age", notice)
	}
	notice := originsecret.FormatStaleNotice(aging, mintedAt.Add(originsecret.MaxAge+24*time.Hour), environment.TierProduction)
	for _, want := range []string{"91 days", "ocel bootstrap"} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice = %q, want it to contain %q", notice, want)
		}
	}
	if strings.Contains(notice, "s1") {
		t.Errorf("notice = %q, which contains the secret itself", notice)
	}

	rotatedAt := mintedAt.Add(originsecret.MaxAge)
	rotating := originsecret.Secret{Current: "s2", CreatedAt: rotatedAt, Previous: "s1", RotatedAt: rotatedAt}
	notice = originsecret.FormatStaleNotice(rotating, rotatedAt.Add(2*24*time.Hour), environment.TierProduction)
	for _, want := range []string{"2 days ago", rotatedAt.Add(originsecret.Grace).Format(time.DateOnly)} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice = %q, want it to contain %q", notice, want)
		}
	}
	for _, secret := range []string{"s1", "s2"} {
		if strings.Contains(notice, secret) {
			t.Errorf("notice = %q, which contains the secret itself", notice)
		}
	}
	if notice := originsecret.FormatStaleNotice(originsecret.Secret{}, mintedAt, environment.TierProduction); notice != "" {
		t.Errorf("notice = %q, want none when no secret is recorded", notice)
	}
}

func TestANoticeOnceTheGraceHasRunSaysTheNextBootstrapRetiresTheOldSecretNotADateGone(t *testing.T) {
	t.Parallel()

	rotatedAt := mintedAt.Add(originsecret.MaxAge)
	rotating := originsecret.Secret{Current: "s2", CreatedAt: rotatedAt, Previous: "s1", RotatedAt: rotatedAt}
	notice := originsecret.FormatStaleNotice(rotating, rotatedAt.Add(originsecret.Grace+24*time.Hour), environment.TierProduction)

	for _, want := range []string{"8 days ago", "next `ocel bootstrap` retires"} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice = %q, want it to contain %q", notice, want)
		}
	}
	if past := rotatedAt.Add(originsecret.Grace).Format(time.DateOnly); strings.Contains(notice, past) {
		t.Errorf("notice = %q, which asks for a re-deploy by %s, a date already gone", notice, past)
	}
}
