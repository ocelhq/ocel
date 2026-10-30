package payloads

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/images"
)

func TestPayloads(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload func() Payload
		entry   string
	}{
		{"runtime layer x86_64", runtimeLayerFor(arch.X8664), "bootstrap"},
		{"runtime layer arm64", runtimeLayerFor(arch.ARM64), "bootstrap"},
		{"upload completer", UploadCompleter, "bootstrap"},
		{"env source sync", EnvSourceSync, "bootstrap"},
		{"realtime authorizer", RealtimeAuthorizer, "bootstrap"},
		{"image optimizer", ImageOptimizer, "index.mjs"},
		{"tag invalidator", TagInvalidator, "index.mjs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.payload()
			if len(p.Bytes) == 0 {
				t.Fatal("has no bytes")
			}
			sum := sha256.Sum256(p.Bytes)
			if want := hex.EncodeToString(sum[:]); p.SHA256 != want {
				t.Errorf("SHA256 = %q, want %q", p.SHA256, want)
			}
			if want := base64.StdEncoding.EncodeToString(sum[:]); p.ChecksumSHA256 != want {
				t.Errorf("ChecksumSHA256 = %q, want %q", p.ChecksumSHA256, want)
			}
			r, err := zip.NewReader(bytes.NewReader(p.Bytes), int64(len(p.Bytes)))
			if err != nil {
				t.Fatalf("read as a zip: %v", err)
			}
			found := false
			for _, f := range r.File {
				if f.Name == tc.entry {
					found = true
				}
			}
			if !found {
				t.Errorf("contains no %s", tc.entry)
			}
		})
	}
}

func runtimeLayerFor(architecture string) func() Payload {
	return func() Payload {
		layer, err := RuntimeLayer(architecture)
		if err != nil {
			panic(err)
		}
		return layer
	}
}

func TestTheRuntimeShipsForEveryArchitectureAFunctionRunsOn(t *testing.T) {
	for _, architecture := range []string{arch.X8664, arch.ARM64} {
		if _, err := RuntimeLayer(architecture); err != nil {
			t.Errorf("RuntimeLayer(%q) = %v, want the runtime built for it", architecture, err)
		}
	}
	if _, err := RuntimeLayer("riscv"); err == nil {
		t.Error("RuntimeLayer(riscv) = nil error, want a refusal: nothing is built for it")
	}
}

func TestPayloadsDiffer(t *testing.T) {
	seen := map[string]string{}
	for name, p := range map[string]Payload{
		"runtime layer x86_64": runtimeLayerFor(arch.X8664)(),
		"runtime layer arm64":  runtimeLayerFor(arch.ARM64)(),
		"upload completer":     UploadCompleter(),
		"env source sync":      EnvSourceSync(),
		"image optimizer":      ImageOptimizer(),
		"tag invalidator":      TagInvalidator(),
	} {
		if other, ok := seen[p.SHA256]; ok {
			t.Errorf("%s and %s have the same bytes", name, other)
		}
		seen[p.SHA256] = name
	}
}

func TestTheRuntimeLayerShipsNoNextContainerRuntime(t *testing.T) {
	for _, architecture := range []string{arch.X8664, arch.ARM64} {
		p := runtimeLayerFor(architecture)()
		r, err := zip.NewReader(bytes.NewReader(p.Bytes), int64(len(p.Bytes)))
		if err != nil {
			t.Fatalf("read the %s layer as a zip: %v", architecture, err)
		}
		for _, f := range r.File {
			if strings.HasPrefix(f.Name, "ocel/container/") {
				t.Errorf("the %s layer ships %s, which only a Next container built from its function boots", architecture, f.Name)
			}
		}
	}
}

func TestTheNextContainerRuntimeBootsTheNextRuntimeOverHTTP(t *testing.T) {
	files := NextContainerRuntime()
	entry := files[images.RuntimeEntrypointFile]
	if len(entry) == 0 || len(files) != 1 {
		t.Fatalf("NextContainerRuntime() = %d files with %d bytes of %s, want that entrypoint alone: a container built from a Next function boots node over it", len(files), len(entry), images.RuntimeEntrypointFile)
	}
	if !bytes.Contains(entry, []byte("PORT")) {
		t.Error("the Next container runtime reads no PORT, so it serves nothing the container runtime forwards to")
	}
}

func TestADigestReadWithoutItsPayloadIsTheDigestOfThePayload(t *testing.T) {
	t.Parallel()

	for name, digests := range map[string][2]string{
		"env source sync": {EnvSourceSyncSHA256(), EnvSourceSync().SHA256},
		"image optimizer": {ImageOptimizerSHA256(), ImageOptimizer().SHA256},
		"tag invalidator": {TagInvalidatorSHA256(), TagInvalidator().SHA256},
	} {
		if digests[0] != digests[1] {
			t.Errorf("the %s digest = %q, want %q, the digest of the payload it names", name, digests[0], digests[1])
		}
	}
	for _, architecture := range []string{arch.X8664, arch.ARM64} {
		digest, err := RuntimeLayerSHA256(architecture)
		if err != nil {
			t.Fatal(err)
		}
		if layer := runtimeLayerFor(architecture)(); digest != layer.SHA256 {
			t.Errorf("the %s runtime layer digest = %q, want %q", architecture, digest, layer.SHA256)
		}
	}
}
