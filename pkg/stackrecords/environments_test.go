package stackrecords_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func TestEnsureAliasTokenKeepsTheTokenAPreviewWasCreatedWith(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()

	first, err := stackrecords.EnsureAliasToken(ctx, store, environment.TierPreview, "shop", "pr-12", "aaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("EnsureAliasToken: %v", err)
	}
	again, err := stackrecords.EnsureAliasToken(ctx, store, environment.TierPreview, "shop", "pr-12", "bbbbbbbbbbbbbbbb")
	if err != nil {
		t.Fatalf("EnsureAliasToken again: %v", err)
	}
	if first != "aaaaaaaaaaaaaaaa" || again != first {
		t.Errorf("EnsureAliasToken = %q then %q, want the first token kept: a preview's alias never moves", first, again)
	}
}

func TestEnsureLifecycleClaimsThePreviewWithTheAliasTokenItsDeployIsServedOn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	const token = "aaaaaaaaaaaaaaaa"
	if _, err := stackrecords.EnsureAliasToken(ctx, store, environment.TierPreview, "shop", "pr-12", token); err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.RecordBuiltAliasToken(ctx, store, environment.TierPreview, "shop", "pr-12", token); err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.ForgetUnclaimedAlias(ctx, store, environment.TierPreview, "shop", "pr-12", token); err != nil {
		t.Fatal(err)
	}

	if err := stackrecords.EnsureLifecycle(ctx, store, environment.TierPreview, "shop", "pr-12", stackrecords.LifecycleEphemeral, token); err != nil {
		t.Fatalf("EnsureLifecycle: %v", err)
	}

	meta, err := stackrecords.ReadEnvironmentMeta(ctx, store, environment.TierPreview, "shop", "pr-12")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Lifecycle != stackrecords.LifecycleEphemeral || meta.AliasToken != token {
		t.Errorf("pr-12 records %q on %q, want it claimed ephemeral on %q, the token its deploy is served on", meta.Lifecycle, meta.AliasToken, token)
	}
}

func TestEnsureLifecycleIsRefusedWhenThePreviewWasGivenAnotherAliasToken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if _, err := stackrecords.EnsureAliasToken(ctx, store, environment.TierPreview, "shop", "pr-12", "bbbbbbbbbbbbbbbb"); err != nil {
		t.Fatal(err)
	}

	err := stackrecords.EnsureLifecycle(ctx, store, environment.TierPreview, "shop", "pr-12", stackrecords.LifecycleEphemeral, "aaaaaaaaaaaaaaaa")

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Errorf("EnsureLifecycle = %v, want a busy refusal: pr-12 is on another alias than the one this deploy is served on", err)
	}
}

const claimedToken = "aaaaaaaaaaaaaaaa"

func claimPreview(t *testing.T, store keyvalue.Store) {
	t.Helper()
	if err := stackrecords.EnsureLifecycle(context.Background(), store, environment.TierPreview, "shop", "pr-12", stackrecords.LifecycleEphemeral, claimedToken); err != nil {
		t.Fatalf("EnsureLifecycle: %v", err)
	}
}

func TestRecordAliasesIsRefusedAndRecordsNothingWhenThePreviewWasRemovedAfterItsDeployClaimedIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	claimPreview(t, store)
	if err := keyvalue.Forget(ctx, store, stackrecords.EnvironmentKey(environment.TierPreview, "shop", "pr-12")); err != nil {
		t.Fatal(err)
	}

	_, _, err := stackrecords.RecordAliases(ctx, store, environment.TierPreview, "shop", "pr-12", claimedToken,
		[]edge.PreviewHost{{Hostname: "a.preview.acme.com", App: "web"}})

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Errorf("RecordAliases = %v, want a busy refusal: pr-12 was removed after its deploy claimed it", err)
	}
	if _, err := store.Read(ctx, stackrecords.EnvironmentKey(environment.TierPreview, "shop", "pr-12")); !errors.Is(err, keyvalue.ErrNotFound) {
		t.Errorf("reading pr-12 after the refusal = %v, want it still removed", err)
	}
}

func TestRecordAliasesIsRefusedWhenThePreviewWasClaimedAgainOnAnotherAliasToken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	claimPreview(t, store)

	_, _, err := stackrecords.RecordAliases(ctx, store, environment.TierPreview, "shop", "pr-12", "bbbbbbbbbbbbbbbb",
		[]edge.PreviewHost{{Hostname: "b.preview.acme.com", App: "web"}})

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Errorf("RecordAliases = %v, want a busy refusal: pr-12 is claimed on another alias token than this deploy's", err)
	}
	meta, err := stackrecords.ReadEnvironmentMeta(ctx, store, environment.TierPreview, "shop", "pr-12")
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.Aliases) != 0 {
		t.Errorf("pr-12 lists %v, want none of another deploy's aliases", meta.Aliases)
	}
}

func TestRecordAliasesKeepsEveryReplacedAliasUntilItIsWithdrawn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	host := func(hostname string) edge.PreviewHost { return edge.PreviewHost{Hostname: hostname, App: "web"} }
	record := func(aliases ...edge.PreviewHost) []edge.PreviewHost {
		t.Helper()
		_, superseded, err := stackrecords.RecordAliases(ctx, store, environment.TierPreview, "shop", "pr-12", claimedToken, aliases)
		if err != nil {
			t.Fatalf("RecordAliases: %v", err)
		}
		return superseded
	}

	claimPreview(t, store)
	if superseded := record(host("a.preview.acme.com")); len(superseded) != 0 {
		t.Errorf("the first aliases superseded %v, want nothing", superseded)
	}
	record(host("b.preview.acme.com"))
	superseded := record(host("a.preview.acme.com"), host("c.preview.acme.com"))
	if want := []edge.PreviewHost{host("b.preview.acme.com")}; !slices.Equal(superseded, want) {
		t.Errorf("superseded = %v, want %v: b was replaced and a is served again", superseded, want)
	}

	if err := stackrecords.ForgetSuperseded(ctx, store, environment.TierPreview, "shop", "pr-12", superseded); err != nil {
		t.Fatalf("ForgetSuperseded: %v", err)
	}
	meta, err := stackrecords.ReadEnvironmentMeta(ctx, store, environment.TierPreview, "shop", "pr-12")
	if err != nil {
		t.Fatalf("ReadEnvironmentMeta: %v", err)
	}
	if meta.AliasToken != claimedToken || len(meta.Superseded) != 0 {
		t.Errorf("meta = %+v, want the alias token kept and nothing left to withdraw", meta)
	}
	if want := []edge.PreviewHost{host("a.preview.acme.com"), host("c.preview.acme.com")}; !slices.Equal(meta.Aliases, want) {
		t.Errorf("aliases = %v, want %v", meta.Aliases, want)
	}
}

func TestRestoreAliasesListsThePreviousAliasesAndKeepsTheUnservedOnesToWithdraw(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	host := func(hostname string) edge.PreviewHost { return edge.PreviewHost{Hostname: hostname, App: "web"} }
	a, b := host("a.preview.acme.com"), host("b.moved.acme.com")
	claimPreview(t, store)

	if _, _, err := stackrecords.RecordAliases(ctx, store, environment.TierPreview, "shop", "pr-12", claimedToken, []edge.PreviewHost{a}); err != nil {
		t.Fatalf("RecordAliases: %v", err)
	}
	previous, _, err := stackrecords.RecordAliases(ctx, store, environment.TierPreview, "shop", "pr-12", claimedToken, []edge.PreviewHost{b})
	if err != nil {
		t.Fatalf("RecordAliases: %v", err)
	}
	if err := stackrecords.RestoreAliases(ctx, store, environment.TierPreview, "shop", "pr-12", claimedToken, []edge.PreviewHost{b}, previous); err != nil {
		t.Fatalf("RestoreAliases: %v", err)
	}
	meta, err := stackrecords.ReadEnvironmentMeta(ctx, store, environment.TierPreview, "shop", "pr-12")
	if err != nil {
		t.Fatalf("ReadEnvironmentMeta: %v", err)
	}
	if !slices.Equal(meta.Aliases, []edge.PreviewHost{a}) || !slices.Equal(meta.Superseded, []edge.PreviewHost{b}) {
		t.Errorf("meta = %+v, want a listed and b kept to withdraw, since a failed move may have published it", meta)
	}
}

func TestRestoreAliasesLeavesTheAliasesAnOverlappingDeployRecordedSinceAndKeepsItsOwnToWithdraw(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	host := func(hostname string) edge.PreviewHost { return edge.PreviewHost{Hostname: hostname, App: "web"} }
	a, b, c := host("a.preview.acme.com"), host("b.moved.acme.com"), host("c.again.acme.com")
	claimPreview(t, store)

	if _, _, err := stackrecords.RecordAliases(ctx, store, environment.TierPreview, "shop", "pr-12", claimedToken, []edge.PreviewHost{a}); err != nil {
		t.Fatalf("RecordAliases: %v", err)
	}
	previous, _, err := stackrecords.RecordAliases(ctx, store, environment.TierPreview, "shop", "pr-12", claimedToken, []edge.PreviewHost{b})
	if err != nil {
		t.Fatalf("RecordAliases: %v", err)
	}
	_, superseded, err := stackrecords.RecordAliases(ctx, store, environment.TierPreview, "shop", "pr-12", claimedToken, []edge.PreviewHost{c})
	if err != nil {
		t.Fatalf("RecordAliases: %v", err)
	}
	if err := stackrecords.ForgetSuperseded(ctx, store, environment.TierPreview, "shop", "pr-12", superseded); err != nil {
		t.Fatalf("ForgetSuperseded: %v", err)
	}
	if err := stackrecords.RestoreAliases(ctx, store, environment.TierPreview, "shop", "pr-12", claimedToken, []edge.PreviewHost{b}, previous); err != nil {
		t.Fatalf("RestoreAliases: %v", err)
	}
	meta, err := stackrecords.ReadEnvironmentMeta(ctx, store, environment.TierPreview, "shop", "pr-12")
	if err != nil {
		t.Fatalf("ReadEnvironmentMeta: %v", err)
	}
	if !slices.Equal(meta.Aliases, []edge.PreviewHost{c}) || !slices.Equal(meta.Superseded, []edge.PreviewHost{b}) {
		t.Errorf("meta = %+v, want c listed as the deploy that succeeded recorded it, and b kept to withdraw since the failed move may have published it", meta)
	}
}
