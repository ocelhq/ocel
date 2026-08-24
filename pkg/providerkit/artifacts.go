package providerkit

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type ArtifactStore interface {
	Put(ctx context.Context, ref ArtifactRef, body io.Reader) error

	Open(ctx context.Context, ref ArtifactRef) (io.ReadCloser, error)

	RemovePrefix(ctx context.Context, class Class, prefix string, report Reporter) error
}

type ArtifactRef struct {
	Class  Class  `json:"class,omitempty"`
	Bucket string `json:"bucket,omitempty"`
	Key    string `json:"key,omitempty"`
}

const (
	StoreFunctions = "functions"
	StoreAssets    = "assets"
	StoreCache     = "cache"
)

type ArtifactPacker interface {
	PackApp(ctx context.Context, packing AppPacking, report Reporter) (AppPack, error)
}

type AppPacking struct {
	Ref       StackRef
	App       string
	Framework string
	Values    AppValues
}

type AppPack struct {
	Overlay map[string][]byte

	Carry any
}

func (r *deployRun) upload(ctx context.Context, report Reporter) error {
	if len(r.manifest.GetFunctions()) == 0 {
		return nil
	}
	source, carries := r.provider.(MembraneSource)
	if !carries {
		return nil
	}
	ref, err := PlaceMembrane(ctx, source, r.plan.Class, r.provider.Artifacts(), report)
	if err != nil {
		return err
	}
	r.membrane = ref
	return nil
}

func (r *deployRun) pack(ctx context.Context, entry AppEntry, values AppValues, report Reporter) (AppPack, error) {
	packer, packs := r.provider.(ArtifactPacker)
	if !packs {
		return AppPack{}, nil
	}
	pack, err := packer.PackApp(ctx, AppPacking{
		Ref:       r.ref(entry.Stack),
		App:       entry.App,
		Framework: entry.Manifest.GetFramework(),
		Values:    values,
	}, report)
	if err != nil {
		return AppPack{}, fmt.Errorf("pack %s's function package: %w", entry.App, err)
	}
	return pack, nil
}

func (r *deployRun) uploadApp(ctx context.Context, entry AppEntry, pack AppPack, routing *RoutingPlan, report Reporter) error {
	root := ArtifactRoot()
	for _, fn := range r.manifest.GetFunctions() {
		if fn.GetApp() != entry.App {
			continue
		}
		ref, err := r.put(ctx, root, entry, fn, overlayFor(pack.Overlay, fn, routing), report)
		if err != nil {
			return err
		}
		r.artifacts[fn.GetLogicalName()] = ref
	}
	return nil
}

func overlayFor(base map[string][]byte, fn *contractv1.ManifestFunction, routing *RoutingPlan) map[string][]byte {
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

func (r *deployRun) put(
	ctx context.Context,
	root string,
	entry AppEntry,
	fn *contractv1.ManifestFunction,
	overlay map[string][]byte,
	report Reporter,
) (ArtifactRef, error) {
	name := fn.GetLogicalName()
	if fn.GetArtifactPath() == "" {
		return ArtifactRef{}, Refuse(CodeInvalid, "function %s names no build artifact, so there is nothing to ship", name)
	}
	dir := filepath.Join(root, filepath.FromSlash(fn.GetArtifactPath()))
	sum, err := digestArtifact(dir, overlay)
	if err != nil {
		return ArtifactRef{}, fmt.Errorf("read %s's artifact: %w", name, err)
	}
	coordinate := r.plan.coordinate(entry.App, entry.Build.Release())
	coordinate.Name = name
	ref := ArtifactRef{Class: r.plan.Class, Bucket: StoreFunctions, Key: coordinate.FunctionArtifactKey(sum)}

	body, err := packArtifact(dir, overlay)
	if err != nil {
		return ArtifactRef{}, fmt.Errorf("pack %s's artifact: %w", name, err)
	}

	report.Say("Uploading " + name)
	if err := r.provider.Artifacts().Put(ctx, ref, bytes.NewReader(body)); err != nil {
		return ArtifactRef{}, fmt.Errorf("upload %s's artifact: %w", name, err)
	}
	return ref, nil
}

func artifactFiles(dir string) ([]string, error) {
	var rels []string
	if err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() && entry.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		return nil, err
	}
	slices.Sort(rels)
	return rels, nil
}

func overlayFiles(overlay map[string][]byte) []string {
	return slices.Sorted(maps.Keys(overlay))
}

const artifactDigestLen = 16

func digestArtifact(dir string, overlay map[string][]byte) (string, error) {
	rels, err := artifactFiles(dir)
	if err != nil {
		return "", err
	}
	sum := sha256.New()
	for _, rel := range rels {
		full := filepath.Join(dir, rel)
		info, err := os.Lstat(full)
		if err != nil {
			return "", err
		}
		writeLenPrefixed(sum, []byte(rel))

		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(full)
			if err != nil {
				return "", err
			}
			sum.Write([]byte{2})
			writeLenPrefixed(sum, []byte(target))
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
	for _, rel := range overlayFiles(overlay) {
		writeLenPrefixed(sum, []byte(rel))
		sum.Write([]byte{0})
		writeLenPrefixed(sum, overlay[rel])
	}
	return hex.EncodeToString(sum.Sum(nil))[:artifactDigestLen], nil
}

func packArtifact(dir string, overlay map[string][]byte) ([]byte, error) {
	rels, err := artifactFiles(dir)
	if err != nil {
		return nil, err
	}
	var packed bytes.Buffer
	archive := zip.NewWriter(&packed)
	for _, rel := range rels {
		full := filepath.Join(dir, rel)
		info, err := os.Lstat(full)
		if err != nil {
			return nil, err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return nil, err
		}
		header.Name = rel
		header.Method = zip.Deflate
		entry, err := archive.CreateHeader(header)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(full)
			if err != nil {
				return nil, err
			}
			if _, err := io.WriteString(entry, target); err != nil {
				return nil, err
			}
			continue
		}
		if err := copyInto(entry, full); err != nil {
			return nil, err
		}
	}
	for _, rel := range overlayFiles(overlay) {
		entry, err := archive.Create(rel)
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write(overlay[rel]); err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return packed.Bytes(), nil
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

func environmentTier(class Class) environmentv1.Tier {
	if class == ClassPreview {
		return environmentv1.Tier_TIER_PREVIEW
	}
	return environmentv1.Tier_TIER_PRODUCTION
}
