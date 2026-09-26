package bootstrap

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

var secretMintedAt = time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)

func storedOriginSecret(t *testing.T, ssmc *fakeSSM, name string) OriginSecret {
	t.Helper()
	raw, ok := ssmc.params[name]
	if !ok {
		t.Fatalf("%s stores nothing", name)
	}
	stored, err := OriginSecretOf(raw)
	if err != nil {
		t.Fatalf("%s = %q: %v", name, raw, err)
	}
	return stored
}

func recordOriginSecret(t *testing.T, ssmc *fakeSSM, name string, secret OriginSecret) {
	t.Helper()
	raw, err := json.Marshal(secret)
	if err != nil {
		t.Fatal(err)
	}
	ssmc.params[name] = string(raw)
}

func TestEnsureOriginSecret(t *testing.T) {
	t.Run("mints once and records when", func(t *testing.T) {
		ssmc := newFakeSSM()

		outcome, err := ensureOriginSecret(context.Background(), ssmc, defaultNamespace, ClassProduction, secretMintedAt)
		if err != nil {
			t.Fatalf("ensureOriginSecret: %v", err)
		}
		if !outcome.minted || outcome.rotated || outcome.retired {
			t.Errorf("outcome = %+v, want a plain mint", outcome)
		}
		first := storedOriginSecret(t, ssmc, originSecretParam)
		if _, err := hex.DecodeString(first.Current); err != nil || len(first.Current) != 64 {
			t.Fatalf("secret = %q, want 32 random bytes the front can send as a header", first.Current)
		}
		if !first.CreatedAt.Equal(secretMintedAt) || first.Rotating() {
			t.Errorf("stored = %+v, want the mint time recorded and no predecessor", first)
		}

		outcome, err = ensureOriginSecret(context.Background(), ssmc, defaultNamespace, ClassProduction, secretMintedAt.Add(24*time.Hour))
		if err != nil {
			t.Fatalf("ensureOriginSecret (second run): %v", err)
		}
		if outcome.minted || outcome.rotated || ssmc.puts != 1 {
			t.Errorf("second bootstrap = %+v after %d puts, want the stored secret reused: rotating it early strands every promoted pointer", outcome, ssmc.puts)
		}
		if again := storedOriginSecret(t, ssmc, originSecretParam); again.Current != first.Current {
			t.Errorf("second bootstrap stored %q, want %q kept", again.Current, first.Current)
		}

		if _, err := ensureOriginSecret(context.Background(), ssmc, defaultNamespace, ClassPreview, secretMintedAt); err != nil {
			t.Fatalf("ensureOriginSecret (preview): %v", err)
		}
		if preview := storedOriginSecret(t, ssmc, previewOriginSecret); preview.Current == first.Current {
			t.Error("preview and production share a secret; a preview front must not reach a production release")
		}
	})

	t.Run("converges on a concurrent bootstrap", func(t *testing.T) {
		winner, _ := json.Marshal(OriginSecret{Current: "the-other-bootstraps-secret", CreatedAt: secretMintedAt})
		ssmc := &racingSSM{fakeSSM: newFakeSSM(), winner: string(winner)}

		outcome, err := ensureOriginSecret(context.Background(), ssmc, defaultNamespace, ClassProduction, secretMintedAt)
		if err != nil {
			t.Fatalf("ensureOriginSecret lost a race instead of converging: %v", err)
		}
		if outcome.minted {
			t.Error("the loser of the race reports a mint of its own")
		}
		if stored := storedOriginSecret(t, ssmc.fakeSSM, originSecretParam); stored.Current != "the-other-bootstraps-secret" {
			t.Errorf("secret = %q, want the winner's", stored.Current)
		}
	})

	t.Run("refuses a class it has no parameter for", func(t *testing.T) {
		if _, err := ensureOriginSecret(context.Background(), newFakeSSM(), defaultNamespace, "staging", secretMintedAt); err == nil {
			t.Error("an unknown class minted a secret, want the class refused")
		}
	})

	t.Run("refuses a parameter storing something other than the record it writes", func(t *testing.T) {
		ssmc := newFakeSSM()
		ssmc.params[originSecretParam] = "5e884898da28047151d0e56f8dc6292773603d0d"

		_, err := ensureOriginSecret(context.Background(), ssmc, defaultNamespace, ClassProduction, secretMintedAt)
		if err == nil || !strings.Contains(err.Error(), originSecretParam) {
			t.Fatalf("err = %v, want the parameter named so the operator can delete it", err)
		}
		if ssmc.puts != 0 {
			t.Error("an unreadable parameter was overwritten, which strands every release deployed with what it stored")
		}
	})
}

func TestOriginSecretRotation(t *testing.T) {
	minted := func(t *testing.T) *fakeSSM {
		t.Helper()
		ssmc := newFakeSSM()
		recordOriginSecret(t, ssmc, originSecretParam, OriginSecret{Current: "s1", CreatedAt: secretMintedAt})
		return ssmc
	}

	t.Run("a secret younger than the maximum age is kept", func(t *testing.T) {
		ssmc := minted(t)

		outcome, err := ensureOriginSecret(context.Background(), ssmc, defaultNamespace, ClassProduction, secretMintedAt.Add(OriginSecretMaxAge-time.Second))
		if err != nil {
			t.Fatalf("ensureOriginSecret: %v", err)
		}
		if outcome != (originSecretOutcome{}) || ssmc.puts != 0 {
			t.Errorf("outcome = %+v after %d puts, want nothing touched", outcome, ssmc.puts)
		}
	})

	t.Run("a secret at the maximum age is rotated: a successor is minted and the old one kept beside it for the releases still presenting it", func(t *testing.T) {
		ssmc := minted(t)
		rotatedAt := secretMintedAt.Add(OriginSecretMaxAge)

		outcome, err := ensureOriginSecret(context.Background(), ssmc, defaultNamespace, ClassProduction, rotatedAt)
		if err != nil {
			t.Fatalf("ensureOriginSecret: %v", err)
		}
		if !outcome.rotated || !outcome.minted || outcome.retired {
			t.Errorf("outcome = %+v, want a rotation", outcome)
		}
		stored := storedOriginSecret(t, ssmc, originSecretParam)
		if stored.Current == "s1" || len(stored.Current) != 64 {
			t.Errorf("current = %q, want a fresh secret", stored.Current)
		}
		if stored.Previous != "s1" || !stored.RotatedAt.Equal(rotatedAt) || !stored.CreatedAt.Equal(rotatedAt) {
			t.Errorf("stored = %+v, want s1 kept as the predecessor from %v", stored, rotatedAt)
		}
	})

	t.Run("the predecessor is retired once its grace has run", func(t *testing.T) {
		ssmc := newFakeSSM()
		rotatedAt := secretMintedAt.Add(OriginSecretMaxAge)
		recordOriginSecret(t, ssmc, originSecretParam, OriginSecret{Current: "s2", CreatedAt: rotatedAt, Previous: "s1", RotatedAt: rotatedAt})

		outcome, err := ensureOriginSecret(context.Background(), ssmc, defaultNamespace, ClassProduction, rotatedAt.Add(OriginSecretGrace))
		if err != nil {
			t.Fatalf("ensureOriginSecret: %v", err)
		}
		if !outcome.retired || outcome.rotated || outcome.minted {
			t.Errorf("outcome = %+v, want the predecessor retired and nothing minted", outcome)
		}
		stored := storedOriginSecret(t, ssmc, originSecretParam)
		if stored.Current != "s2" || stored.Rotating() || !stored.RotatedAt.IsZero() {
			t.Errorf("stored = %+v, want s2 alone", stored)
		}
	})

	t.Run("a predecessor still within its grace is left in place", func(t *testing.T) {
		ssmc := newFakeSSM()
		rotatedAt := secretMintedAt.Add(OriginSecretMaxAge)
		recordOriginSecret(t, ssmc, originSecretParam, OriginSecret{Current: "s2", CreatedAt: rotatedAt, Previous: "s1", RotatedAt: rotatedAt})

		outcome, err := ensureOriginSecret(context.Background(), ssmc, defaultNamespace, ClassProduction, rotatedAt.Add(OriginSecretGrace-time.Hour))
		if err != nil {
			t.Fatalf("ensureOriginSecret: %v", err)
		}
		if outcome != (originSecretOutcome{}) || ssmc.puts != 0 {
			t.Errorf("outcome = %+v after %d puts, want the rotation left to run", outcome, ssmc.puts)
		}
	})

	t.Run("a rotation due while the last one is still in grace refuses rather than strand twice", func(t *testing.T) {
		ssmc := newFakeSSM()
		recordOriginSecret(t, ssmc, originSecretParam, OriginSecret{Current: "s2", CreatedAt: secretMintedAt, Previous: "s1", RotatedAt: secretMintedAt.Add(OriginSecretMaxAge - time.Hour)})

		_, err := ensureOriginSecret(context.Background(), ssmc, defaultNamespace, ClassProduction, secretMintedAt.Add(OriginSecretMaxAge))
		if err == nil || !strings.Contains(err.Error(), "re-deploy every project") {
			t.Fatalf("err = %v, want a refusal that says what to do", err)
		}
		if ssmc.puts != 0 {
			t.Error("a refused rotation still wrote the parameter")
		}
	})

	t.Run("a rotation whose predecessor's grace has run retires it and rotates in one run", func(t *testing.T) {
		ssmc := newFakeSSM()
		recordOriginSecret(t, ssmc, originSecretParam, OriginSecret{Current: "s2", CreatedAt: secretMintedAt, Previous: "s1", RotatedAt: secretMintedAt})

		outcome, err := ensureOriginSecret(context.Background(), ssmc, defaultNamespace, ClassProduction, secretMintedAt.Add(OriginSecretMaxAge))
		if err != nil {
			t.Fatalf("ensureOriginSecret: %v", err)
		}
		if !outcome.retired || !outcome.rotated {
			t.Errorf("outcome = %+v, want s1 retired and s2 succeeded", outcome)
		}
		stored := storedOriginSecret(t, ssmc, originSecretParam)
		if stored.Previous != "s2" || stored.Current == "s2" || ssmc.puts != 1 {
			t.Errorf("stored = %+v after %d puts, want s2 as the predecessor of a fresh secret in one write", stored, ssmc.puts)
		}
	})
}

func TestOriginSecretPresentedToARelease(t *testing.T) {
	rotatedAt := secretMintedAt.Add(OriginSecretMaxAge)
	rotating := OriginSecret{Current: "s2", CreatedAt: rotatedAt, Previous: "s1", RotatedAt: rotatedAt}

	if got := rotating.Presented(rotatedAt.Unix() - 1); got != "s1" {
		t.Errorf("a release deployed before the rotation is presented %q, want the secret it was deployed with", got)
	}
	if got := rotating.Presented(rotatedAt.Unix()); got != "s2" {
		t.Errorf("a release deployed at the rotation is presented %q, want the current secret it accepts", got)
	}
	single := OriginSecret{Current: "s2", CreatedAt: rotatedAt}
	if got := single.Presented(0); got != "s2" {
		t.Errorf("after the predecessor is retired a release is presented %q, want the only secret left", got)
	}
}

func TestStaleOriginSecretNotice(t *testing.T) {
	aging := OriginSecret{Current: "s1", CreatedAt: secretMintedAt}

	if notice := StaleOriginSecretNotice(aging, secretMintedAt.Add(OriginSecretMaxAge-time.Second), ClassProduction); notice != "" {
		t.Errorf("notice = %q, want none for a secret within its age", notice)
	}
	notice := StaleOriginSecretNotice(aging, secretMintedAt.Add(OriginSecretMaxAge+24*time.Hour), ClassProduction)
	for _, want := range []string{"91 days", "ocel bootstrap"} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice = %q, want it to contain %q", notice, want)
		}
	}
	if strings.Contains(notice, "s1") {
		t.Errorf("notice = %q, which contains the secret itself", notice)
	}

	rotatedAt := secretMintedAt.Add(OriginSecretMaxAge)
	rotating := OriginSecret{Current: "s2", CreatedAt: rotatedAt, Previous: "s1", RotatedAt: rotatedAt}
	notice = StaleOriginSecretNotice(rotating, rotatedAt.Add(2*24*time.Hour), ClassProduction)
	for _, want := range []string{"2 days ago", rotatedAt.Add(OriginSecretGrace).Format(time.DateOnly)} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice = %q, want it to contain %q", notice, want)
		}
	}
	for _, secret := range []string{"s1", "s2"} {
		if strings.Contains(notice, secret) {
			t.Errorf("notice = %q, which contains the secret itself", notice)
		}
	}
	if notice := StaleOriginSecretNotice(OriginSecret{}, secretMintedAt, ClassProduction); notice != "" {
		t.Errorf("notice = %q, want none when no secret is recorded", notice)
	}
}

func TestPlanOriginSecret(t *testing.T) {
	rotatedAt := secretMintedAt.Add(OriginSecretMaxAge)
	for _, tc := range []struct {
		name     string
		existing *OriginSecret
		now      time.Time
		action   provider.ChangeAction
	}{
		{"absent is created", nil, secretMintedAt, provider.ActionCreate},
		{"young is kept", &OriginSecret{Current: "s1", CreatedAt: secretMintedAt}, secretMintedAt.Add(time.Hour), provider.ActionKeep},
		{"old is rotated", &OriginSecret{Current: "s1", CreatedAt: secretMintedAt}, rotatedAt, provider.ActionUpdate},
		{"in grace is kept", &OriginSecret{Current: "s2", CreatedAt: rotatedAt, Previous: "s1", RotatedAt: rotatedAt}, rotatedAt.Add(time.Hour), provider.ActionKeep},
		{"past grace retires", &OriginSecret{Current: "s2", CreatedAt: rotatedAt, Previous: "s1", RotatedAt: rotatedAt}, rotatedAt.Add(OriginSecretGrace), provider.ActionUpdate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ssmc := newFakeSSM()
			if tc.existing != nil {
				recordOriginSecret(t, ssmc, originSecretParam, *tc.existing)
			}
			change, err := planOriginSecret(context.Background(), ssmc, defaultNamespace, ClassProduction, tc.now)
			if err != nil {
				t.Fatalf("planOriginSecret: %v", err)
			}
			if change.Name != originSecretParam || change.Action != tc.action {
				t.Errorf("change = %+v, want %s as %q", change, originSecretParam, tc.action)
			}
			if tc.action == provider.ActionUpdate && change.Reason == "" {
				t.Error("an update names no reason, so the plan cannot say what the rotation does")
			}
		})
	}
}

func TestBootstrapParamsIncludeTheOriginSecret(t *testing.T) {
	params := fullProductionParams()
	rotatedAt := secretMintedAt.Add(OriginSecretMaxAge)
	raw, _ := json.Marshal(OriginSecret{Current: "origin-2", CreatedAt: rotatedAt, Previous: "origin-1", RotatedAt: rotatedAt})
	params[originSecretParam] = string(raw)

	got, err := ReadClassParams(context.Background(), &fakeBatchSSM{params: params}, defaultNamespace, ClassProduction, KindCloudflare)
	if err != nil {
		t.Fatalf("ReadClassParams: %v", err)
	}
	if got.OriginSecret.Current != "origin-2" || got.OriginSecret.Previous != "origin-1" || !got.OriginSecret.RotatedAt.Equal(rotatedAt) {
		t.Errorf("OriginSecret = %+v, want the rotation bootstrap recorded", got.OriginSecret)
	}

	core, err := ReadCoreParams(context.Background(), &fakeBatchSSM{params: params}, defaultNamespace, ClassProduction)
	if err != nil {
		t.Fatalf("ReadCoreParams: %v", err)
	}
	if core.OriginSecret != got.OriginSecret {
		t.Errorf("ReadCoreParams = %+v, want the same record ReadClassParams reads", core.OriginSecret)
	}

	params[originSecretParam] = "origin-1"
	bare, err := ReadClassParams(context.Background(), &fakeBatchSSM{params: params}, defaultNamespace, ClassProduction, KindCloudflare)
	if err != nil {
		t.Fatalf("ReadClassParams over a bare value = %v, want the read itself to succeed: a teardown presents no secret", err)
	}
	if bare.OriginSecretErr == nil || !strings.Contains(bare.OriginSecretErr.Error(), originSecretParam) || !strings.Contains(bare.OriginSecretErr.Error(), "ocel bootstrap") {
		t.Errorf("OriginSecretErr = %v; want the failure kept, naming the parameter and the bootstrap that replaces it, so a deploy never bakes in something the front will not present", bare.OriginSecretErr)
	}

	delete(params, originSecretParam)
	absent, err := ReadClassParams(context.Background(), &fakeBatchSSM{params: params}, defaultNamespace, ClassProduction, KindCloudflare)
	if err != nil {
		t.Fatalf("ReadClassParams without the secret: %v", err)
	}
	if absent.OriginSecret.Present() {
		t.Errorf("OriginSecret = %+v, want none when the parameter is absent", absent.OriginSecret)
	}

	names, err := ClassParamNames(defaultNamespace, ClassProduction)
	if err != nil {
		t.Fatalf("ClassParamNames: %v", err)
	}
	if !slices.Contains(names, originSecretParam) {
		t.Errorf("teardown deletes %v, want the origin secret among them", names)
	}
}
