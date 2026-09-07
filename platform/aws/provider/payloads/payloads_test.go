package payloads

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestPayloads(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload func() Payload
		entry   string
	}{
		{"membrane layer x86_64", membraneLayerFor(providerkit.ArchX8664), "ocel/bootstrap"},
		{"membrane layer arm64", membraneLayerFor(providerkit.ArchARM64), "ocel/bootstrap"},
		{"upload completer", UploadCompleter, "bootstrap"},
		{"image optimizer", ImageOptimizer, "index.mjs"},
		{"revalidator", Revalidator, "index.mjs"},
		{"tag publisher", TagPublisher, "index.mjs"},
		{"tag invalidator", TagInvalidator, "index.mjs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.payload()
			if len(p.Bytes) == 0 {
				t.Fatal("carries no bytes")
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
				t.Errorf("holds no %s", tc.entry)
			}
		})
	}
}

func membraneLayerFor(arch string) func() Payload {
	return func() Payload {
		layer, err := MembraneLayer(arch)
		if err != nil {
			panic(err)
		}
		return layer
	}
}

func TestTheMembraneIsCarriedForEveryArchitectureAFunctionRunsOn(t *testing.T) {
	for _, arch := range []string{providerkit.ArchX8664, providerkit.ArchARM64} {
		if _, err := MembraneLayer(arch); err != nil {
			t.Errorf("MembraneLayer(%q) = %v, want the membrane built for it", arch, err)
		}
	}
	if _, err := MembraneLayer("riscv"); err == nil {
		t.Error("MembraneLayer(riscv) = nil error, want a refusal: nothing is built for it")
	}
}

func TestPayloadsDiffer(t *testing.T) {
	seen := map[string]string{}
	for name, p := range map[string]Payload{
		"membrane layer x86_64": membraneLayerFor(providerkit.ArchX8664)(),
		"membrane layer arm64":  membraneLayerFor(providerkit.ArchARM64)(),
		"upload completer":      UploadCompleter(),
		"image optimizer":       ImageOptimizer(),
		"revalidator":           Revalidator(),
		"tag publisher":         TagPublisher(),
		"tag invalidator":       TagInvalidator(),
	} {
		if other, ok := seen[p.SHA256]; ok {
			t.Errorf("%s and %s carry the same bytes", name, other)
		}
		seen[p.SHA256] = name
	}
}
