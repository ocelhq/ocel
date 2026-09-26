package payloads

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/arch"
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
		{"image optimizer", ImageOptimizer, "index.mjs"},
		{"revalidator", Revalidator, "index.mjs"},
		{"tag publisher", TagPublisher, "index.mjs"},
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
		"image optimizer":      ImageOptimizer(),
		"revalidator":          Revalidator(),
		"tag publisher":        TagPublisher(),
		"tag invalidator":      TagInvalidator(),
	} {
		if other, ok := seen[p.SHA256]; ok {
			t.Errorf("%s and %s have the same bytes", name, other)
		}
		seen[p.SHA256] = name
	}
}
