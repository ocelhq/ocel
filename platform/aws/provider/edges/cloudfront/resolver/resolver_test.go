package resolver_test

import (
	"bytes"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront/resolver"
)

var update = flag.Bool("update", false, "rewrite minified/preview.js from the sources")

const functionCodeLimit = 10 * 1024

const gateCookie = "__Host-ocel-preview"

const cloudFrontImport = "import cf from 'cloudfront';\n"

const minifiedPreview = "minified/preview.js"

func minifyPreview(t *testing.T) []byte {
	t.Helper()
	read := func(name string) string {
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(source)
	}
	body, imported := strings.CutPrefix(read("src/resolver.js"), cloudFrontImport)
	if !imported {
		t.Fatalf("src/resolver.js does not start with %q, the import the preview program is minified around", cloudFrontImport)
	}
	result := api.Transform(body+"\n"+read("src/previewgate.js"), api.TransformOptions{
		Loader:            api.LoaderJS,
		Target:            api.ES2017,
		Charset:           api.CharsetASCII,
		MinifyWhitespace:  true,
		MinifyIdentifiers: true,
		MinifySyntax:      true,
	})
	if len(result.Errors) > 0 {
		t.Fatalf("the preview resolver does not minify: %s", result.Errors[0].Text)
	}
	return append([]byte(cloudFrontImport), result.Code...)
}

func TestThePreviewResolverIsTheMinifiedResolverAndGate(t *testing.T) {
	want := minifyPreview(t)
	if *update {
		if err := os.WriteFile(minifiedPreview, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(resolver.PreviewCode(), want) {
		t.Errorf("%s is not src/resolver.js and src/previewgate.js minified; run `go test ./platform/aws/provider/edges/cloudfront/resolver -run %s -update`", minifiedPreview, t.Name())
	}
}

func TestEveryResolverFitsTheCloudFrontFunctionCodeLimit(t *testing.T) {
	for name, code := range map[string][]byte{
		"Code()":        resolver.Code(),
		"PreviewCode()": resolver.PreviewCode(),
	} {
		if got := len(code); got > functionCodeLimit {
			t.Errorf("len(%s) = %d bytes, want at most %d: CloudFront refuses a larger function", name, got, functionCodeLimit)
		}
	}
}

func TestThePreviewResolverKeepsTheNamesCloudFrontAndTheGateLookUp(t *testing.T) {
	code := string(resolver.PreviewCode())
	if !strings.HasPrefix(code, cloudFrontImport) {
		t.Errorf("PreviewCode() starts %.40q, want the cloudfront import first", code)
	}
	for _, name := range []string{"async function handler(", "async function gatePreview(", "async function readKey("} {
		if !strings.Contains(code, name) {
			t.Errorf("PreviewCode() has no %q: minifying renamed what the runtime or the resolver looks up by name", name)
		}
	}
}

func TestOnlyThePreviewResolverCarriesTheGate(t *testing.T) {
	if strings.Contains(string(resolver.Code()), gateCookie) {
		t.Errorf("Code() names the %s cookie; a production front is never gated", gateCookie)
	}
	if !strings.Contains(string(resolver.PreviewCode()), gateCookie) {
		t.Errorf("PreviewCode() never names the %s cookie, so it carries no gate", gateCookie)
	}
}
