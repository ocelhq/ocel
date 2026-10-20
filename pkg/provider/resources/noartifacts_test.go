package resources

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func storelessRef() provider.ArtifactRef {
	return provider.ArtifactRef{Tier: environment.TierProduction, Key: "fn/shop/prod/web/r1a2b3c4d/api/bundle.zip"}
}

func TestTheStorelessStoreRefusesAWriteItCannotHonour(t *testing.T) {
	t.Parallel()

	var refused refusal.Refusal
	err := NoArtifacts{}.Put(context.Background(), storelessRef(), bytes.NewReader([]byte("a build artifact")))
	if !errors.As(err, &refused) {
		t.Fatalf("Put() = %v, want a refusal rather than a write that reports success and loses the bytes", err)
	}
	if refused.Code != refusal.CodeInvalid {
		t.Errorf("Put() refused with %q, want %q", refused.Code, refusal.CodeInvalid)
	}
	if !strings.Contains(refused.Message, "artifact store") {
		t.Errorf("Put() refusal = %q, want it to name the shape of the provider that refused", refused.Message)
	}
}

func TestTheStorelessStoreRefusesAReadRatherThanAnsweringEmpty(t *testing.T) {
	t.Parallel()

	var refused refusal.Refusal
	opened, err := NoArtifacts{}.Open(context.Background(), storelessRef())
	if !errors.As(err, &refused) {
		if err == nil {
			opened.Close()
		}
		t.Fatalf("Open() = %v, want a refusal: a missing artifact must never read as an empty one", err)
	}
	if refused.Code != refusal.CodeInvalid {
		t.Errorf("Open() refused with %q, want %q", refused.Code, refusal.CodeInvalid)
	}
}

func TestTheStorelessStoreAnswersHasWithAPlainFalse(t *testing.T) {
	t.Parallel()

	present, err := NoArtifacts{}.Has(context.Background(), storelessRef())
	if err != nil {
		t.Fatalf("Has() = %v, want a plain false: it is the gate plan synthesis draws its create row from", err)
	}
	if present {
		t.Error("Has() claims a store that keeps nothing has an artifact")
	}
}

func TestTheStorelessStoreSweepsAnyScopeWithoutComplaint(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		shop, err := naming.ProjectScope("shop")
		if err != nil {
			t.Fatal(err)
		}
		for _, scope := range []naming.StorageScope{{}, shop} {
			if err := (NoArtifacts{}).RemoveObjects(ctx, tier, scope, naming.CoordinateObjectKinds, nil); err != nil {
				t.Errorf("RemoveObjects(%s, %+v) = %v, want nil: teardown sweeps it on every destroy and every preview reap", tier, scope, err)
			}
		}
	}
}
