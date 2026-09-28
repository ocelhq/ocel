package depstest

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const repo = "github.com/ocelhq/ocel"

var OpenToPkg = []string{
	repo + "/pkg",
}

var ClosedToPkg = []string{
	"ocel.dev",
	"github.com/aws",
	"github.com/awslabs",
	"cloud.google.com/go",
	"google.golang.org/api",
	"github.com/googleapis/gax-go",
	"github.com/cloudflare/cloudflare-go",
	"github.com/Azure/azure-sdk-for-go",
	"github.com/pulumi/pulumi-aws",
	"github.com/pulumi/pulumi-gcp",
	"github.com/pulumi/pulumi-cloudflare",
}

var systems = []string{"linux", "darwin", "windows"}

func Check(t *testing.T, pattern string, open, closed []string) {
	t.Helper()

	for _, goos := range systems {
		t.Run(goos, func(t *testing.T) {
			t.Parallel()

			for _, pkg := range reached(t, goos, pattern) {
				if Within(pkg, []string{repo}) && !Within(pkg, open) {
					t.Errorf("%s on %s reaches %s, which is not among the packages of this repo open to it", pattern, goos, pkg)
				}
				if Within(pkg, closed) {
					t.Errorf("%s on %s reaches %s, which is closed to it", pattern, goos, pkg)
				}
			}
		})
	}
}

func reached(t *testing.T, goos, pattern string) []string {
	t.Helper()

	cmd := exec.Command("go", "list", "-e", "-deps", "-test", "-json=ImportPath,Error", pattern)
	cmd.Env = append(os.Environ(), "GOOS="+goos)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("GOOS=%s go list -deps -test %s: %v\n%s", goos, pattern, err, stderr.Bytes())
	}

	var pkgs []string
	for dec := json.NewDecoder(bytes.NewReader(out)); dec.More(); {
		var p struct {
			ImportPath string
			Error      *struct{ Err string }
		}
		if err := dec.Decode(&p); err != nil {
			t.Fatalf("GOOS=%s go list -deps -test %s: %v", goos, pattern, err)
		}
		if p.Error != nil && !missingEmbed(p.Error.Err) {
			t.Errorf("GOOS=%s go list %s: %s", goos, p.ImportPath, p.Error.Err)
		}
		path, _, _ := strings.Cut(p.ImportPath, " ")
		pkgs = append(pkgs, strings.TrimSuffix(strings.TrimSuffix(path, ".test"), "_test"))
	}
	return pkgs
}

type Package struct {
	ImportPath   string
	Imports      []string
	TestImports  []string
	XTestImports []string
}

func Workspace(t *testing.T) []Package {
	t.Helper()

	gowork, err := exec.Command("go", "env", "GOWORK").Output()
	if err != nil {
		t.Fatalf("go env GOWORK: %v", err)
	}
	if strings.TrimSpace(string(gowork)) == "" {
		t.Fatal("this reads every module go.work names, and go.work is off")
	}
	dirs, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}/...").Output()
	if err != nil {
		t.Fatalf("go list -m: %v", err)
	}
	patterns := strings.Fields(string(dirs))

	merged := map[string]*Package{}
	for _, goos := range systems {
		cmd := exec.Command("go", append([]string{"list", "-e", "-json=ImportPath,Imports,TestImports,XTestImports,Error"}, patterns...)...)
		cmd.Env = append(os.Environ(), "GOOS="+goos)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("GOOS=%s go list the workspace: %v\n%s", goos, err, stderr.Bytes())
		}
		for dec := json.NewDecoder(bytes.NewReader(out)); dec.More(); {
			var p struct {
				Package
				Error *struct{ Err string }
			}
			if err := dec.Decode(&p); err != nil {
				t.Fatalf("GOOS=%s go list the workspace: %v", goos, err)
			}
			if p.Error != nil && !missingEmbed(p.Error.Err) && !excludedByBuildConstraints(p.Error.Err) {
				t.Errorf("GOOS=%s go list %s: %s", goos, p.ImportPath, p.Error.Err)
			}
			m, ok := merged[p.ImportPath]
			if !ok {
				m = &Package{ImportPath: p.ImportPath}
				merged[p.ImportPath] = m
			}
			m.Imports = append(m.Imports, p.Imports...)
			m.TestImports = append(m.TestImports, p.TestImports...)
			m.XTestImports = append(m.XTestImports, p.XTestImports...)
		}
	}

	pkgs := make([]Package, 0, len(merged))
	for _, p := range merged {
		pkgs = append(pkgs, Package{
			ImportPath:   p.ImportPath,
			Imports:      slices.Compact(slices.Sorted(slices.Values(p.Imports))),
			TestImports:  slices.Compact(slices.Sorted(slices.Values(p.TestImports))),
			XTestImports: slices.Compact(slices.Sorted(slices.Values(p.XTestImports))),
		})
	}
	return pkgs
}

func excludedByBuildConstraints(listErr string) bool {
	return strings.HasPrefix(listErr, "build constraints exclude all Go files in ")
}

func missingEmbed(listErr string) bool {
	return strings.HasPrefix(listErr, "pattern ") && strings.HasSuffix(listErr, ": no matching files found")
}

func Within(pkg string, roots []string) bool {
	for _, root := range roots {
		if pkg == root || strings.HasPrefix(pkg, root+"/") {
			return true
		}
	}
	return false
}
