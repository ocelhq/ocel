package resolver_test

import (
	"bytes"
	"flag"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/ocelhq/ocel/pkg/edge/gate"

	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront/resolver"
)

var update = flag.Bool("update", false, "rewrite minified/resolver.js from the sources")

const functionCodeLimit = 10 * 1024

const cloudFrontImport = "import cf from 'cloudfront';\n"

const minified = "minified/resolver.js"

func minify(t *testing.T) []byte {
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
		t.Fatalf("src/resolver.js does not start with %q, the import the resolver is minified around", cloudFrontImport)
	}
	result := api.Transform(body+"\n"+read("src/gate.js")+"\nexport { handler };\n", api.TransformOptions{
		Loader:            api.LoaderJS,
		Format:            api.FormatESModule,
		Target:            api.ES2017,
		Charset:           api.CharsetASCII,
		MinifyWhitespace:  true,
		MinifyIdentifiers: true,
		MinifySyntax:      true,
	})
	if len(result.Errors) > 0 {
		t.Fatalf("the resolver does not minify: %s", result.Errors[0].Text)
	}
	code := string(result.Code)
	exported := handlerExport.FindStringSubmatch(code)
	if exported == nil {
		t.Fatalf("the minified resolver does not end by exporting handler: %.80q", code[max(0, len(code)-80):])
	}
	code = strings.TrimSuffix(code, exported[0])
	declared := "async function " + exported[1] + "("
	if strings.Count(code, declared) != 1 || strings.Contains(code, "handler") {
		t.Fatalf("the minified resolver declares handler as %q, which cannot be renamed back unambiguously", exported[1])
	}
	return append([]byte(cloudFrontImport), strings.Replace(code, declared, "async function handler(", 1)...)
}

var handlerExport = regexp.MustCompile(`export\{(\w+) as handler\};?\n?$`)

func TestTheResolverIsTheMinifiedResolverAndGate(t *testing.T) {
	want := minify(t)
	if *update {
		if err := os.WriteFile(minified, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(resolver.Code(), want) {
		t.Errorf("%s is not src/resolver.js and src/gate.js minified; run `go test ./platform/aws/provider/edges/cloudfront/resolver -run %s -update`", minified, t.Name())
	}
}

func TestTheResolverFitsTheCloudFrontFunctionCodeLimit(t *testing.T) {
	if got := len(resolver.Code()); got > functionCodeLimit {
		t.Errorf("len(Code()) = %d bytes, want at most %d: CloudFront refuses a larger function", got, functionCodeLimit)
	}
}

func TestTheResolverKeepsTheNameCloudFrontCalls(t *testing.T) {
	code := string(resolver.Code())
	if !strings.HasPrefix(code, cloudFrontImport) {
		t.Errorf("Code() starts %.40q, want the cloudfront import first", code)
	}
	if !strings.Contains(code, "async function handler(") {
		t.Errorf("Code() declares no handler: minifying renamed the function CloudFront calls by name")
	}
}

func TestTheResolverCarriesTheGateWireNames(t *testing.T) {
	code := string(resolver.Code())
	for _, name := range []string{gate.CookieName, gate.LoginPath, gate.HeaderBypass, gate.HeaderSetBypassCookie} {
		if !strings.Contains(code, name) {
			t.Errorf("Code() never names %q, so the gate it carries does not answer the one every other edge runs", name)
		}
	}
}
