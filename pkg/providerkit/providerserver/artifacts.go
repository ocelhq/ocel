package providerserver

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"

	"golang.org/x/sync/errgroup"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	"github.com/ocelhq/ocel/pkg/providerkit/images"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (r *deployRun) pack(ctx context.Context, entry provider.AppEntry, values provider.AppValues, progress edge.Progress) (provider.AppPack, error) {
	packApp := r.provider.Hooks().PackApp
	if packApp == nil {
		return provider.AppPack{}, nil
	}
	pack, err := packApp(ctx, provider.AppPacking{
		Ref:    r.ref(entry.Stack),
		Edge:   r.front.Kind(),
		App:    entry.App,
		Values: values,
	}, progress)
	if err != nil {
		return provider.AppPack{}, fmt.Errorf("pack %s's function package: %w", entry.App, err)
	}
	return pack, nil
}

func (r *deployRun) stageFunctions(
	ctx context.Context,
	entry provider.AppEntry,
	pack provider.AppPack,
	routing *provider.RoutingPlan,
) ([]provider.Upload, []images.Push, error) {
	if hooks := r.provider.Hooks(); hooks.FunctionImages != nil {
		pushes, err := r.imageFunctions(ctx, hooks, entry, pack, routing)
		return nil, pushes, err
	}
	staged, err := r.stageApp(entry, pack, routing)
	return staged, nil, err
}

func (r *deployRun) stageApp(entry provider.AppEntry, pack provider.AppPack, routing *provider.RoutingPlan) ([]provider.Upload, error) {
	root := appbuild.ArtifactRoot()
	var shipping []*contractv1.ManifestFunction
	for _, fn := range r.manifest.GetFunctions() {
		if fn.GetApp() == entry.App {
			shipping = append(shipping, fn)
		}
	}
	staged := make([]provider.Upload, len(shipping))
	var group errgroup.Group
	group.SetLimit(resources.UploadConcurrency)
	for slot, fn := range shipping {
		group.Go(func() error {
			defer resources.TakeUploadSlot()()
			upload, err := r.stageArtifact(root, entry, fn, overlayFor(pack.Overlay, fn, routing))
			staged[slot] = upload
			return err
		})
	}
	if err := group.Wait(); err != nil {
		discardStaged(staged)
		return nil, err
	}
	for slot, fn := range shipping {
		r.recordArtifact(fn.GetLogicalName(), staged[slot].Ref)
	}
	return staged, nil
}

func discardStaged(staged []provider.Upload) {
	for _, upload := range staged {
		if upload.Path != "" {
			os.Remove(upload.Path)
		}
	}
}

func overlayFor(base map[string][]byte, fn *contractv1.ManifestFunction, routing *provider.RoutingPlan) map[string][]byte {
	if routing == nil || routeOf(fn) != routing.Entry {
		return base
	}
	overlay := make(map[string][]byte, len(base)+1)
	maps.Copy(overlay, base)
	overlay[edge.RoutingManifestFile] = routing.Manifest
	return overlay
}

func routeOf(fn *contractv1.ManifestFunction) string {
	if route := fn.GetRouteId(); route != "" {
		return route
	}
	return fn.GetLogicalName()
}

func stagedDir(root string, fn *contractv1.ManifestFunction) (string, error) {
	if fn.GetArtifactPath() == "" {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"function %s names no build artifact, so there is nothing to ship", fn.GetLogicalName())
	}
	return filepath.Join(root, filepath.FromSlash(fn.GetArtifactPath())), nil
}

func (r *deployRun) stageArtifact(
	root string,
	entry provider.AppEntry,
	fn *contractv1.ManifestFunction,
	overlay map[string][]byte,
) (provider.Upload, error) {
	name := fn.GetLogicalName()
	dir, err := stagedDir(root, fn)
	if err != nil {
		return provider.Upload{}, err
	}
	rels, err := images.ArtifactFiles(dir)
	if err != nil {
		return provider.Upload{}, fmt.Errorf("read %s's artifact: %w", name, err)
	}
	sum, err := digestArtifact(dir, rels, overlay)
	if err != nil {
		return provider.Upload{}, fmt.Errorf("read %s's artifact: %w", name, err)
	}
	coordinate := appCoordinate(r.plan, entry.App, entry.Build.Release())
	coordinate.Name = name

	path, err := packArtifact(dir, rels, overlay)
	if err != nil {
		return provider.Upload{}, fmt.Errorf("pack %s's artifact: %w", name, err)
	}

	return provider.Upload{
		Name:   name,
		Ref:    provider.ArtifactRef{Class: r.plan.Class, Bucket: provider.StoreFunctions, Key: coordinate.FunctionArtifactKey(sum)},
		Path:   path,
		Digest: sum,
	}, nil
}

const artifactDigestLen = 16

func digestArtifact(dir string, rels []string, overlay map[string][]byte) (string, error) {
	sum := sha256.New()
	for _, rel := range rels {
		full := filepath.Join(dir, rel)
		info, err := os.Lstat(full)
		if err != nil {
			return "", err
		}
		provider.WriteLenPrefixed(sum, []byte(rel))

		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(full)
			if err != nil {
				return "", err
			}
			sum.Write([]byte{2})
			provider.WriteLenPrefixed(sum, []byte(target))
			continue
		}

		var executable [1]byte
		if info.Mode()&0o100 != 0 {
			executable[0] = 1
		}
		sum.Write(executable[:])

		file, err := os.Open(full)
		if err != nil {
			return "", err
		}
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(info.Size()))
		sum.Write(size[:])
		_, err = io.Copy(sum, file)
		file.Close()
		if err != nil {
			return "", err
		}
	}
	for _, rel := range images.OverlayFiles(overlay) {
		provider.WriteLenPrefixed(sum, []byte(rel))
		sum.Write([]byte{0})
		provider.WriteLenPrefixed(sum, overlay[rel])
	}
	return hex.EncodeToString(sum.Sum(nil))[:artifactDigestLen], nil
}

func packArtifact(dir string, rels []string, overlay map[string][]byte) (string, error) {
	packed, err := os.CreateTemp("", "ocel-artifact-*.zip")
	if err != nil {
		return "", err
	}
	if err := writeArchive(packed, dir, rels, overlay); err != nil {
		discard(packed)
		return "", err
	}
	if err := packed.Close(); err != nil {
		os.Remove(packed.Name())
		return "", err
	}
	return packed.Name(), nil
}

func discard(packed *os.File) {
	packed.Close()
	os.Remove(packed.Name())
}

func writeArchive(into io.Writer, dir string, rels []string, overlay map[string][]byte) error {
	archive := zip.NewWriter(into)
	for _, rel := range rels {
		full := filepath.Join(dir, rel)
		info, err := os.Lstat(full)
		if err != nil {
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = rel
		header.Method = zip.Deflate
		entry, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(full)
			if err != nil {
				return err
			}
			if _, err := io.WriteString(entry, target); err != nil {
				return err
			}
			continue
		}
		if err := copyInto(entry, full); err != nil {
			return err
		}
	}
	for _, rel := range images.OverlayFiles(overlay) {
		entry, err := archive.Create(rel)
		if err != nil {
			return err
		}
		if _, err := entry.Write(overlay[rel]); err != nil {
			return err
		}
	}
	return archive.Close()
}

func copyInto(entry io.Writer, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	_, err = io.Copy(entry, file)
	file.Close()
	return err
}

func environmentTier(class edge.Class) environmentv1.Tier {
	if class == edge.ClassPreview {
		return environmentv1.Tier_TIER_PREVIEW
	}
	return environmentv1.Tier_TIER_PRODUCTION
}
