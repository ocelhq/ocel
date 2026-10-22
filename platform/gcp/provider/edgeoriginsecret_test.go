package gcp

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/originsecret"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudflareorigin"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
)

var originSecretMintedAt = time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)

func (h *offersHarness) originSecretName(tier environment.Tier) string {
	return h.clients.OriginSecret(tier, cloudflareKind)
}

func (h *offersHarness) ensureOriginSecret(t *testing.T, tier environment.Tier, now time.Time) error {
	t.Helper()
	return cloudflareorigin.EnsureOriginSecret(t.Context(), h.clients, tier, cloudflareKind, now, h.log)
}

func (h *offersHarness) storedOriginSecret(t *testing.T, tier environment.Tier) originsecret.Secret {
	t.Helper()
	raw, found := h.secrets.Latest(t, h.originSecretName(tier))
	if !found {
		t.Fatalf("%s stores nothing", h.originSecretName(tier))
	}
	stored, err := originsecret.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("%s = %q: %v", h.originSecretName(tier), raw, err)
	}
	return stored
}

func (h *offersHarness) recordOriginSecret(t *testing.T, payload []byte) {
	t.Helper()
	service, err := h.clients.Secrets()
	if err != nil {
		t.Fatal(err)
	}
	name := h.originSecretName(environment.TierProduction)
	if err := h.clients.EnsureSecret(t.Context(), service, environment.TierProduction, name); err != nil {
		t.Fatal(err)
	}
	if _, err := h.clients.AddSecretVersion(t.Context(), service, name, payload); err != nil {
		t.Fatal(err)
	}
}

func (h *offersHarness) recordOriginSecretAs(t *testing.T, secret originsecret.Secret) int {
	t.Helper()
	raw, err := json.Marshal(secret)
	if err != nil {
		t.Fatal(err)
	}
	h.recordOriginSecret(t, raw)
	return h.secrets.VersionsAdded()
}

func TestABootstrapMintsTheOriginSecretOnceAndKeepsItWhileItIsYoung(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)

	if err := h.ensureOriginSecret(t, environment.TierProduction, originSecretMintedAt); err != nil {
		t.Fatalf("EnsureOriginSecret() = %v", err)
	}
	first := h.storedOriginSecret(t, environment.TierProduction)
	if len(first.Current) != 64 || !first.CreatedAt.Equal(originSecretMintedAt) || first.Rotating() {
		t.Fatalf("stored = %+v, want a fresh secret minted at %v with no predecessor", first, originSecretMintedAt)
	}

	if err := h.ensureOriginSecret(t, environment.TierProduction, originSecretMintedAt.Add(24*time.Hour)); err != nil {
		t.Fatalf("EnsureOriginSecret() (second run) = %v", err)
	}
	if again := h.storedOriginSecret(t, environment.TierProduction); again != first || h.secrets.VersionsAdded() != 1 {
		t.Errorf("a second bootstrap stored %+v after %d versions, want %+v kept: rotating it early strands every release", again, h.secrets.VersionsAdded(), first)
	}

	if err := h.ensureOriginSecret(t, environment.TierPreview, originSecretMintedAt); err != nil {
		t.Fatalf("EnsureOriginSecret() (preview) = %v", err)
	}
	if preview := h.storedOriginSecret(t, environment.TierPreview); preview.Current == first.Current {
		t.Error("preview and production share a secret; a preview edge must not reach a production release")
	}
}

func TestABootstrapThatLostTheRaceToMintTheOriginSecretKeepsTheWinners(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	winner, _ := json.Marshal(originsecret.Secret{Current: "the-other-bootstraps-secret", CreatedAt: originSecretMintedAt})
	h.secrets.WrittenFirstElsewhere = map[string]string{h.originSecretName(environment.TierProduction): string(winner)}

	if err := h.ensureOriginSecret(t, environment.TierProduction, originSecretMintedAt); err != nil {
		t.Fatalf("EnsureOriginSecret() lost a race instead of converging: %v", err)
	}
	if stored := h.storedOriginSecret(t, environment.TierProduction); stored.Current != "the-other-bootstraps-secret" || h.secrets.VersionsAdded() != 0 {
		t.Errorf("stored = %+v after %d versions added, want the winner's secret alone", stored, h.secrets.VersionsAdded())
	}
}

func TestABootstrapRotatesAStaleOriginSecretKeepingTheOneItReplaces(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	h.recordOriginSecretAs(t, originsecret.Secret{Current: "s1", CreatedAt: originSecretMintedAt})
	rotatedAt := originSecretMintedAt.Add(originsecret.MaxAge)

	if err := h.ensureOriginSecret(t, environment.TierProduction, rotatedAt); err != nil {
		t.Fatalf("EnsureOriginSecret() = %v", err)
	}

	stored := h.storedOriginSecret(t, environment.TierProduction)
	if stored.Current == "s1" || len(stored.Current) != 64 {
		t.Errorf("current = %q, want a fresh secret", stored.Current)
	}
	if stored.Previous != "s1" || !stored.RotatedAt.Equal(rotatedAt) || !stored.CreatedAt.Equal(rotatedAt) {
		t.Errorf("stored = %+v, want s1 kept as the predecessor from %v", stored, rotatedAt)
	}
	if enabled := h.secrets.EnabledVersions(h.originSecretName(environment.TierProduction)); len(enabled) != 1 {
		t.Errorf("the secret keeps the enabled versions %v, want only the record that carries both secrets", enabled)
	}
	if lines := strings.Join(h.log.Lines(), "\n"); !strings.Contains(lines, "WARN Rotated the production origin secret") {
		t.Errorf("the bootstrap said %q, want a warning that the tier's projects must be re-deployed", lines)
	}
}

func TestABootstrapRetiresThePreviousOriginSecretOnceItsGraceHasRun(t *testing.T) {
	t.Parallel()
	rotatedAt := originSecretMintedAt.Add(originsecret.MaxAge)
	rotating := originsecret.Secret{Current: "s2", CreatedAt: rotatedAt, Previous: "s1", RotatedAt: rotatedAt}

	t.Run("within the grace it is kept", func(t *testing.T) {
		t.Parallel()
		h := newOffersHarness(t)
		added := h.recordOriginSecretAs(t, rotating)

		if err := h.ensureOriginSecret(t, environment.TierProduction, rotatedAt.Add(originsecret.Grace-time.Hour)); err != nil {
			t.Fatalf("EnsureOriginSecret() = %v", err)
		}
		if stored := h.storedOriginSecret(t, environment.TierProduction); stored != rotating || h.secrets.VersionsAdded() != added {
			t.Errorf("stored = %+v, want the rotation left to run untouched", stored)
		}
	})

	t.Run("after the grace it is retired", func(t *testing.T) {
		t.Parallel()
		h := newOffersHarness(t)
		h.recordOriginSecretAs(t, rotating)

		if err := h.ensureOriginSecret(t, environment.TierProduction, rotatedAt.Add(originsecret.Grace)); err != nil {
			t.Fatalf("EnsureOriginSecret() = %v", err)
		}
		if stored := h.storedOriginSecret(t, environment.TierProduction); stored != (originsecret.Secret{Current: "s2", CreatedAt: rotatedAt}) {
			t.Errorf("stored = %+v, want s2 alone", stored)
		}
		if lines := strings.Join(h.log.Lines(), "\n"); !strings.Contains(lines, "WARN Retired the production origin secret") {
			t.Errorf("the bootstrap said %q, want a warning that releases not re-deployed stop answering", lines)
		}
	})
}

func TestABootstrapRefusesAnOriginSecretStoringSomethingOtherThanTheRecordItWrites(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	h.recordOriginSecret(t, []byte("5e884898da28047151d0e56f8dc6292773603d0d"))
	added := h.secrets.VersionsAdded()

	err := h.ensureOriginSecret(t, environment.TierProduction, originSecretMintedAt)

	if err == nil || !strings.Contains(err.Error(), h.originSecretName(environment.TierProduction)) {
		t.Fatalf("EnsureOriginSecret() = %v, want the secret named so the operator can delete it", err)
	}
	if strings.Contains(err.Error(), "5e884898") {
		t.Errorf("the refusal %q contains what the secret stores", err)
	}
	if h.secrets.VersionsAdded() != added {
		t.Error("an unreadable secret was overwritten, which strands every release deployed with what it stored")
	}
}

func TestReadingTheOriginSecretReturnsTheRecordBootstrapWrote(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)

	if _, found, err := cloudflareorigin.ReadOriginSecret(t.Context(), h.clients, environment.TierProduction, cloudflareKind); err != nil || found {
		t.Fatalf("ReadOriginSecret() before bootstrap = %v, %v, want nothing found", found, err)
	}
	if err := h.ensureOriginSecret(t, environment.TierProduction, originSecretMintedAt); err != nil {
		t.Fatal(err)
	}
	read, found, err := cloudflareorigin.ReadOriginSecret(t.Context(), h.clients, environment.TierProduction, cloudflareKind)
	if err != nil || !found || read != h.storedOriginSecret(t, environment.TierProduction) {
		t.Errorf("ReadOriginSecret() = %+v, %v, %v, want the record bootstrap stored", read, found, err)
	}
}

func TestAPlanSaysWhatABootstrapDoesToTheOriginSecret(t *testing.T) {
	t.Parallel()
	rotatedAt := originSecretMintedAt.Add(originsecret.MaxAge)
	for _, tc := range []struct {
		name     string
		existing *originsecret.Secret
		now      time.Time
		action   provider.ChangeAction
	}{
		{"absent is created", nil, originSecretMintedAt, provider.ActionCreate},
		{"young is kept", &originsecret.Secret{Current: "s1", CreatedAt: originSecretMintedAt}, originSecretMintedAt.Add(time.Hour), provider.ActionKeep},
		{"old is rotated", &originsecret.Secret{Current: "s1", CreatedAt: originSecretMintedAt}, rotatedAt, provider.ActionUpdate},
		{"in grace is kept", &originsecret.Secret{Current: "s2", CreatedAt: rotatedAt, Previous: "s1", RotatedAt: rotatedAt}, rotatedAt.Add(time.Hour), provider.ActionKeep},
		{"past grace retires", &originsecret.Secret{Current: "s2", CreatedAt: rotatedAt, Previous: "s1", RotatedAt: rotatedAt}, rotatedAt.Add(originsecret.Grace), provider.ActionUpdate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newOffersHarness(t)
			if tc.existing != nil {
				h.recordOriginSecretAs(t, *tc.existing)
			}
			added := h.secrets.VersionsAdded()

			change, err := cloudflareorigin.PlannedOriginSecret(t.Context(), h.clients, environment.TierProduction, cloudflareKind, tc.now)
			if err != nil {
				t.Fatalf("PlannedOriginSecret() = %v", err)
			}
			want := provider.Change{Kind: string(bootstrap.KindSecret), Name: h.originSecretName(environment.TierProduction), Action: tc.action}
			if change.Kind != want.Kind || change.Name != want.Name || change.Action != want.Action || (change.Reason == "") != (tc.action == provider.ActionCreate) {
				t.Errorf("change = %+v, want %+v with a reason unless it is a create", change, want)
			}
			if h.secrets.VersionsAdded() != added || (tc.existing == nil && h.secrets.Has(want.Name)) {
				t.Error("planning wrote the secret")
			}
		})
	}
}

func TestRaisingAFrontThatRunsCodeEnsuresItsOriginSecretAndTearingItDownDeletesIt(t *testing.T) {
	t.Parallel()
	h := newAssetStoreHarness(t)
	b, registry := frontWithAssetReader(h)
	name := h.clients.OriginSecret(environment.TierProduction, registry.front.Kind())

	if err := b.RaiseFronts(t.Context(), provider.BootstrapRequest{Tier: environment.TierProduction, Parts: []string{bootstrap.CloudflareOriginPart}}, nil); err != nil {
		t.Fatalf("RaiseFronts() = %v", err)
	}
	if _, found, err := cloudflareorigin.ReadOriginSecret(t.Context(), h.clients, environment.TierProduction, registry.front.Kind()); err != nil || !found {
		t.Fatalf("after the raise ReadOriginSecret() = %v, %v, want the secret every release of the tier demands of the edge", found, err)
	}

	if err := b.TearFronts(t.Context(), environment.TierProduction, []string{bootstrap.CloudflareOriginPart}); err != nil {
		t.Fatalf("TearFronts() = %v", err)
	}
	if h.secrets.Has(name) {
		t.Errorf("the %s secret outlived the part that raised it", name)
	}
}

func TestRaisingAFrontThatRunsNoCodeMintsNoOriginSecret(t *testing.T) {
	t.Parallel()
	h := newAssetStoreHarness(t)
	b, registry := frontWithAssetReader(h)
	registry.front.runsCode = false

	if err := b.RaiseFronts(t.Context(), provider.BootstrapRequest{Tier: environment.TierProduction, Parts: []string{bootstrap.CloudflareOriginPart}}, nil); err != nil {
		t.Fatalf("RaiseFronts() = %v", err)
	}
	if name := h.clients.OriginSecret(environment.TierProduction, registry.front.Kind()); h.secrets.Has(name) {
		t.Errorf("a front that runs no code was given the %s secret, which nothing it reaches demands", name)
	}
}

func TestAPlanOfAFrontThatRunsCodeNamesItsOriginSecret(t *testing.T) {
	t.Parallel()
	h := newAssetStoreHarness(t)
	b, registry := frontWithAssetReader(h)

	groups, err := b.PlannedFronts(t.Context(), environment.TierProduction, []string{bootstrap.CloudflareOriginPart})
	if err != nil {
		t.Fatalf("PlannedFronts() = %v", err)
	}
	name := h.clients.OriginSecret(environment.TierProduction, registry.front.Kind())
	if len(groups) != 1 || !slices.ContainsFunc(groups[0].Changes, func(change provider.Change) bool {
		return change.Name == name && change.Action == provider.ActionCreate
	}) {
		t.Errorf("the plan = %+v, want the edge group to create %s", groups, name)
	}
}

func TestARemovalPlanOfAFrontThatRunsCodeNamesTheOriginSecretItDeletes(t *testing.T) {
	t.Parallel()
	h := newAssetStoreHarness(t)
	b, registry := frontWithAssetReader(h)
	name := h.clients.OriginSecret(environment.TierProduction, registry.front.Kind())
	deletes := func(groups []provider.ChangeGroup) bool {
		return len(groups) == 1 && slices.ContainsFunc(groups[0].Changes, func(change provider.Change) bool {
			return change.Name == name && change.Action == provider.ActionDelete
		})
	}

	if removed, err := b.RemovedFronts(t.Context(), environment.TierProduction, []string{bootstrap.CloudflareOriginPart}); err != nil || deletes(removed) {
		t.Errorf("the removal plan with no origin secret stored = %+v, %v, want nothing deleting %s", removed, err, name)
	}
	if err := b.RaiseFronts(t.Context(), provider.BootstrapRequest{Tier: environment.TierProduction, Parts: []string{bootstrap.CloudflareOriginPart}}, nil); err != nil {
		t.Fatalf("RaiseFronts() = %v", err)
	}
	removed, err := b.RemovedFronts(t.Context(), environment.TierProduction, []string{bootstrap.CloudflareOriginPart})
	if err != nil {
		t.Fatalf("RemovedFronts() = %v", err)
	}
	if !deletes(removed) {
		t.Errorf("the removal plan = %+v, want the edge group to delete %s, which removing the part does", removed, name)
	}
}
