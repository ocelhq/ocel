package providerkit

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func storelessRef() ArtifactRef {
	return ArtifactRef{Class: ClassProduction, Bucket: StoreFunctions, Key: "shop/prod/web/bundle.zip"}
}

func TestTheStorelessStoreRefusesAWriteItCannotHonour(t *testing.T) {
	t.Parallel()

	var refusal Refusal
	err := NoArtifacts{}.Put(context.Background(), storelessRef(), bytes.NewReader([]byte("a build artifact")))
	if !errors.As(err, &refusal) {
		t.Fatalf("Put() = %v, want a refusal rather than a write that reports success and loses the bytes", err)
	}
	if refusal.Code != CodeInvalid {
		t.Errorf("Put() refused with %q, want %q", refusal.Code, CodeInvalid)
	}
	if !strings.Contains(refusal.Message, "artifact store") {
		t.Errorf("Put() refusal = %q, want it to name the shape of the provider that refused", refusal.Message)
	}
}

func TestTheStorelessStoreRefusesAReadRatherThanAnsweringEmpty(t *testing.T) {
	t.Parallel()

	var refusal Refusal
	opened, err := NoArtifacts{}.Open(context.Background(), storelessRef())
	if !errors.As(err, &refusal) {
		if err == nil {
			opened.Close()
		}
		t.Fatalf("Open() = %v, want a refusal: a missing artifact must never read as an empty one", err)
	}
	if refusal.Code != CodeInvalid {
		t.Errorf("Open() refused with %q, want %q", refusal.Code, CodeInvalid)
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
	for _, class := range []Class{ClassProduction, ClassPreview} {
		for _, prefix := range []string{"", "shop/prod/", "shop/nothing-was-ever-written-here/"} {
			if err := (NoArtifacts{}).RemovePrefix(ctx, class, prefix, nil); err != nil {
				t.Errorf("RemovePrefix(%s, %q) = %v, want nil: teardown sweeps it on every destroy and every preview reap", class, prefix, err)
			}
		}
	}
}

func TestTheStorelessStoreIsAnArtifactStore(t *testing.T) {
	t.Parallel()

	var store ArtifactStore = NoArtifacts{}
	if _, storeless := store.(NoArtifacts); !storeless {
		t.Error("the empty store does not assert back to its own exported type, so the conformance tier cannot detect the shape")
	}
}
