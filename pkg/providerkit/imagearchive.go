package providerkit

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
)

const (
	archiveIndex    = "index.json"
	archiveBlobs    = "blobs/sha256/"
	archiveKeptSize = 4 << 20
)

type ImageArchive struct {
	stream io.Reader
	pipe   *io.PipeWriter
	done   chan error
	once   sync.Once
	gap    error
}

func CompleteArchive(stream io.Reader, daemon, ref string) *ImageArchive {
	reader, writer := io.Pipe()
	a := &ImageArchive{stream: stream, pipe: writer, done: make(chan error, 1)}
	go func() { a.done <- inventory(reader, daemon, ref) }()
	return a
}

func (a *ImageArchive) Read(p []byte) (int, error) {
	n, err := a.stream.Read(p)
	if n > 0 {
		if _, written := a.pipe.Write(p[:n]); written != nil {
			return n, written
		}
	}
	if err != nil {
		a.settle(err)
	}
	return n, err
}

func (a *ImageArchive) settle(err error) {
	a.once.Do(func() {
		if errors.Is(err, io.EOF) {
			_ = a.pipe.Close()
			a.gap = <-a.done
			return
		}
		_ = a.pipe.CloseWithError(err)
		<-a.done
	})
}

func (a *ImageArchive) Gap() error { return a.gap }

type archiveDescriptor struct {
	Digest string `json:"digest"`
}

type archiveManifest struct {
	Config    archiveDescriptor   `json:"config"`
	Layers    []archiveDescriptor `json:"layers"`
	Manifests []archiveDescriptor `json:"manifests"`
}

func inventory(from *io.PipeReader, daemon, ref string) error {
	present := map[string]bool{}
	kept := map[string][]byte{}
	archive := tar.NewReader(from)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_, _ = io.Copy(io.Discard, from)
			return nil
		}
		if digest, blob := strings.CutPrefix(header.Name, archiveBlobs); blob {
			present[digest] = true
		}
		if header.Typeflag == tar.TypeReg && header.Size <= archiveKeptSize {
			content, err := io.ReadAll(archive)
			if err != nil {
				_, _ = io.Copy(io.Discard, from)
				return nil
			}
			kept[header.Name] = content
			continue
		}
		_, _ = io.Copy(io.Discard, archive)
	}
	index, indexed := kept[archiveIndex]
	if !indexed {
		return nil
	}
	missing := map[string]bool{}
	var walk func(document []byte)
	walk = func(document []byte) {
		var manifest archiveManifest
		if json.Unmarshal(document, &manifest) != nil {
			return
		}
		for _, named := range manifest.Manifests {
			digest := strings.TrimPrefix(named.Digest, "sha256:")
			nested, held := kept[archiveBlobs+digest]
			if !held {
				if !present[digest] {
					missing[named.Digest] = true
				}
				continue
			}
			walk(nested)
		}
		for _, named := range append(manifest.Layers, manifest.Config) {
			if named.Digest == "" {
				continue
			}
			if !present[strings.TrimPrefix(named.Digest, "sha256:")] {
				missing[named.Digest] = true
			}
		}
	}
	walk(index)
	if len(missing) == 0 {
		return nil
	}
	lost := make([]string, 0, len(missing))
	for digest := range missing {
		lost = append(lost, digest)
	}
	sort.Strings(lost)
	return Refuse(CodeInvalid,
		"the daemon at %s exported %s without %d of the blobs its manifest names (%s): the daemon no longer holds them, so the archive would load as an image with layers missing — rebuild the image and deploy again",
		daemon, ref, len(lost), strings.Join(lost, ", "))
}

var _ io.Reader = (*ImageArchive)(nil)
