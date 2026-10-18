package gcp

import (
	"context"
	"testing"
)

func TestCloudRunDeclaresNoFunctionSizeBudget(t *testing.T) {
	if got := pushing(t, "").Facts().MaxFunctionBytes; got != 0 {
		t.Errorf("Facts().MaxFunctionBytes = %d, want none: a Cloud Run function is a container image, which no unzipped-code limit splits", got)
	}
}

func TestCloudRunSaysItShipsANextServerRuntime(t *testing.T) {
	if files, err := pushing(t, "").Hooks().NextServerRuntime(context.Background()); err != nil || len(files) == 0 {
		t.Errorf("NextServerRuntime() = %d files, %v, want the Next server runtime a Cloud Run Next container loads its cache handlers from", len(files), err)
	}
}
