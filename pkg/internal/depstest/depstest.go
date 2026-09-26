package depstest

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

const repo = "github.com/ocelhq/ocel"

var OpenToPkg = []string{
	repo + "/pkg",
	repo + "/platform/edge/contract",
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
				if within(pkg, []string{repo}) && !within(pkg, open) {
					t.Errorf("%s on %s reaches %s, which is not among the packages of this repo open to it", pattern, goos, pkg)
				}
				if within(pkg, closed) {
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

func missingEmbed(listErr string) bool {
	return strings.HasPrefix(listErr, "pattern ") && strings.HasSuffix(listErr, ": no matching files found")
}

func within(pkg string, roots []string) bool {
	for _, root := range roots {
		if pkg == root || strings.HasPrefix(pkg, root+"/") {
			return true
		}
	}
	return false
}
