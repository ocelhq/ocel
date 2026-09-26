package providerkit

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func storelessRef() provider.ArtifactRef {
	return provider.ArtifactRef{Class: edge.ClassProduction, Bucket: provider.StoreFunctions, Key: "shop/prod/web/bundle.zip"}
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

	held, err := NoArtifacts{}.Has(context.Background(), storelessRef())
	if err != nil {
		t.Fatalf("Has() = %v, want a plain false: it is the gate plan synthesis draws its create row from", err)
	}
	if held {
		t.Error("Has() claims a store that keeps nothing holds an artifact")
	}
}

func TestTheStorelessStoreSweepsAnyPrefixWithoutComplaint(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	for _, class := range []edge.Class{edge.ClassProduction, edge.ClassPreview} {
		for _, prefix := range []string{"", "shop/prod/", "shop/nothing-was-ever-written-here/"} {
			if err := (NoArtifacts{}).RemovePrefix(ctx, class, prefix, nil); err != nil {
				t.Errorf("RemovePrefix(%s, %q) = %v, want nil: teardown sweeps it on every destroy and every preview reap", class, prefix, err)
			}
		}
	}
}
