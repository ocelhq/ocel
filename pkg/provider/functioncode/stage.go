package functioncode

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"

	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func Stage(spec provider.StackSpec, packed map[string][]byte) ([]Upload, error) {
	if spec.App == nil {
		return nil, nil
	}
	shipping := spec.App.Functions
	staged := make([]Upload, len(shipping))
	var group errgroup.Group
	group.SetLimit(UploadConcurrency)
	for slot, fn := range shipping {
		group.Go(func() error {
			defer TakeUploadSlot()()
			upload, err := stageFunction(spec, fn, withOverlay(packed, fn.Overlay))
			staged[slot] = upload
			return err
		})
	}
	if err := group.Wait(); err != nil {
		Discard(staged)
		return nil, err
	}
	return staged, nil
}

func Refs(uploads []Upload) map[string]provider.ArtifactRef {
	refs := make(map[string]provider.ArtifactRef, len(uploads))
	for _, upload := range uploads {
		refs[upload.Name] = upload.Ref
	}
	return refs
}

func Discard(staged []Upload) {
	for _, upload := range staged {
		if upload.Path != "" {
			os.Remove(upload.Path)
		}
	}
}

func withOverlay(base, own map[string][]byte) map[string][]byte {
	if len(own) == 0 {
		return base
	}
	overlay := make(map[string][]byte, len(base)+len(own))
	maps.Copy(overlay, base)
	maps.Copy(overlay, own)
	return overlay
}

func functionDir(fn provider.FunctionSpec) (string, error) {
	if fn.ArtifactDir == "" {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"function %s names no build artifact, so there is nothing to ship", fn.Name)
	}
	return fn.ArtifactDir, nil
}

func stageFunction(spec provider.StackSpec, fn provider.FunctionSpec, overlay map[string][]byte) (Upload, error) {
	dir, err := functionDir(fn)
	if err != nil {
		return Upload{}, err
	}
	rels, err := images.ArtifactFiles(dir)
	if err != nil {
		return Upload{}, fmt.Errorf("read %s's artifact: %w", fn.Name, err)
	}
	sum, err := digest(dir, rels, overlay)
	if err != nil {
		return Upload{}, fmt.Errorf("read %s's artifact: %w", fn.Name, err)
	}
	coordinate := coordinateOf(spec.Ref)
	coordinate.Name = fn.Name

	path, err := pack(dir, rels, overlay)
	if err != nil {
		return Upload{}, fmt.Errorf("pack %s's artifact: %w", fn.Name, err)
	}
	return Upload{
		Name:   fn.Name,
		Ref:    provider.ArtifactRef{Tier: spec.Ref.Tier, Bucket: provider.StoreFunctions, Key: coordinate.FunctionArtifactKey(sum)},
		Path:   path,
		Digest: sum,
	}, nil
}

func coordinateOf(ref provider.StackRef) naming.Coordinate {
	return naming.Coordinate{
		Project: naming.Sanitize(ref.Project),
		Env:     ref.Name.Env,
		App:     ref.Name.App,
		Release: ref.Name.Release,
	}
}

const digestLen = 16

func digest(dir string, rels []string, overlay map[string][]byte) (string, error) {
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
	return hex.EncodeToString(sum.Sum(nil))[:digestLen], nil
}

func pack(dir string, rels []string, overlay map[string][]byte) (string, error) {
	packed, err := os.CreateTemp("", "ocel-artifact-*.zip")
	if err != nil {
		return "", err
	}
	if err := writeArchive(packed, dir, rels, overlay); err != nil {
		packed.Close()
		os.Remove(packed.Name())
		return "", err
	}
	if err := packed.Close(); err != nil {
		os.Remove(packed.Name())
		return "", err
	}
	return packed.Name(), nil
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
