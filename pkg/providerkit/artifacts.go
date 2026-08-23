package providerkit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type ArtifactStore interface {
	Put(ctx context.Context, ref ArtifactRef, body io.Reader) error

	Open(ctx context.Context, ref ArtifactRef) (io.ReadCloser, error)

	RemovePrefix(ctx context.Context, prefix string, report Reporter) error
}

type ArtifactRef struct {
	Bucket string `json:"bucket,omitempty"`
	Key    string `json:"key,omitempty"`
}

const (
	StoreFunctions = "functions"
	StoreAssets    = "assets"
	StoreCache     = "cache"
)

func (r *deployRun) upload(ctx context.Context, report Reporter) error {
	root := ArtifactRoot()
	for _, entry := range r.plan.Apps {
		for _, fn := range r.manifest.GetFunctions() {
			if fn.GetApp() != "" && fn.GetApp() != entry.App {
				continue
			}
			ref, err := r.put(ctx, root, entry, fn, report)
			if err != nil {
				return err
			}
			r.artifacts[fn.GetLogicalName()] = ref
		}
	}
	if len(r.artifacts) == 0 {
		return nil
	}
	source, carries := r.provider.(MembraneSource)
	if !carries {
		return nil
	}
	ref, err := PlaceMembrane(ctx, source, r.provider.Artifacts(), report)
	if err != nil {
		return err
	}
	r.membrane = ref
	return nil
}

func (r *deployRun) put(
	ctx context.Context,
	root string,
	entry AppEntry,
	fn *contractv1.ManifestFunction,
	report Reporter,
) (ArtifactRef, error) {
	name := fn.GetLogicalName()
	path := filepath.Join(root, filepath.FromSlash(fn.GetArtifactPath()))
	if fn.GetArtifactPath() == "" {
		return ArtifactRef{}, Refuse(CodeInvalid, "function %s names no build artifact, so there is nothing to ship", name)
	}
	sum, err := digestFile(path)
	if err != nil {
		return ArtifactRef{}, err
	}
	coordinate := r.plan.coordinate(entry.App, entry.Build.Release())
	coordinate.Name = name
	ref := ArtifactRef{Bucket: StoreFunctions, Key: coordinate.FunctionArtifactKey(sum)}

	body, err := os.Open(path)
	if err != nil {
		return ArtifactRef{}, fmt.Errorf("read %s's artifact: %w", name, err)
	}
	defer body.Close()

	report.Say("Uploading " + name)
	if err := r.provider.Artifacts().Put(ctx, ref, body); err != nil {
		return ArtifactRef{}, fmt.Errorf("upload %s's artifact: %w", name, err)
	}
	return ref, nil
}

func digestFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	defer file.Close()

	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return hex.EncodeToString(sum.Sum(nil))[:16], nil
}

func environmentTier(class Class) environmentv1.Tier {
	if class == ClassPreview {
		return environmentv1.Tier_TIER_PREVIEW
	}
	return environmentv1.Tier_TIER_PRODUCTION
}
