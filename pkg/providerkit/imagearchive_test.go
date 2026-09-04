package providerkit

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

type fixtureBlob struct {
	digest  string
	content []byte
}

func blobOf(content []byte) fixtureBlob {
	sum := sha256.Sum256(content)
	return fixtureBlob{digest: hex.EncodeToString(sum[:]), content: content}
}

func archiveOf(t *testing.T, blobs []fixtureBlob, index []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	write := func(name string, content []byte) {
		if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	write("oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`))
	for _, blob := range blobs {
		write(archiveBlobs+blob.digest, blob.content)
	}
	write(archiveIndex, index)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func imageFixture(t *testing.T) (config, layer, manifest fixtureBlob, index []byte) {
	t.Helper()
	config = blobOf([]byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`))
	layer = blobOf(bytes.Repeat([]byte("layer bytes "), 1000))
	document, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config":        map[string]any{"digest": "sha256:" + config.digest, "size": len(config.content)},
		"layers":        []map[string]any{{"digest": "sha256:" + layer.digest, "size": len(layer.content)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest = blobOf(document)
	index, err = json.Marshal(map[string]any{
		"schemaVersion": 2,
		"manifests":     []map[string]any{{"digest": "sha256:" + manifest.digest, "mediaType": "application/vnd.oci.image.manifest.v1+json"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return config, layer, manifest, index
}

func TestAWholeArchivePassesThroughUntouchedAndReportsNoGap(t *testing.T) {
	config, layer, manifest, index := imageFixture(t)
	archive := archiveOf(t, []fixtureBlob{config, layer, manifest}, index)

	checked := CompleteArchive(bytes.NewReader(archive), "unix:///var/run/docker.sock", "ocel/web:sha256-abc")
	carried, err := io.ReadAll(checked)
	if err != nil {
		t.Fatalf("reading through the check = %v", err)
	}
	if !bytes.Equal(carried, archive) {
		t.Fatal("the check altered the bytes it carried, and the box would load something other than what the daemon exported")
	}
	if gap := checked.Gap(); gap != nil {
		t.Errorf("Gap() over a whole archive = %v", gap)
	}
}

func TestAnArchiveMissingALayerItsManifestNamesIsRefusedNamingTheDigestAndTheDaemon(t *testing.T) {
	config, layer, manifest, index := imageFixture(t)
	archive := archiveOf(t, []fixtureBlob{config, manifest}, index)

	checked := CompleteArchive(bytes.NewReader(archive), "unix:///var/run/docker.sock", "ocel/web:sha256-abc")
	if _, err := io.ReadAll(checked); err != nil {
		t.Fatalf("reading through the check = %v: the stream itself is carried whole either way", err)
	}
	gap := checked.Gap()
	if gap == nil {
		t.Fatal("Gap() over an archive missing a layer = nil, and the box would be the first to learn the daemon dropped it")
	}
	for _, named := range []string{"sha256:" + layer.digest, "unix:///var/run/docker.sock", "ocel/web:sha256-abc", "rebuild"} {
		if !strings.Contains(gap.Error(), named) {
			t.Errorf("the refusal %q never names %q", gap.Error(), named)
		}
	}
}

func TestAStreamThatIsNoArchiveAtAllIsCarriedAndReportsNothing(t *testing.T) {
	checked := CompleteArchive(strings.NewReader("not a tar"), "unix:///var/run/docker.sock", "ocel/web:sha256-abc")
	if _, err := io.ReadAll(checked); err != nil {
		t.Fatalf("reading through the check = %v", err)
	}
	if gap := checked.Gap(); gap != nil {
		t.Errorf("Gap() over bytes that are no archive = %v, want the daemon's own answer to stand", gap)
	}
}
