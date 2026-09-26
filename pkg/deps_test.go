package pkg_test

import (
	"os/exec"
	"strings"
	"testing"
)

const repo = "github.com/ocelhq/ocel"

var openToPkg = []string{
	repo + "/pkg",
	repo + "/platform/edge/contract",
}

var vendorSDKs = []string{
	"ocel.dev",
	"github.com/aws/aws-sdk-go",
	"github.com/aws/aws-sdk-go-v2",
	"cloud.google.com/go",
	"google.golang.org/api",
	"github.com/cloudflare/cloudflare-go",
	"github.com/Azure/azure-sdk-for-go",
}

func TestPkgImportsOnlyWhatTheCodebaseMapOpensToIt(t *testing.T) {
	t.Parallel()

	for pattern, closed := range map[string][]string{
		"./...":                    append([]string{"github.com/pulumi"}, vendorSDKs...),
		"./providerkit/pulumi/...": vendorSDKs,
	} {
		out, err := exec.Command("go", "list", "-deps", "-test", "-f", "{{.ImportPath}}", pattern).CombinedOutput()
		if err != nil {
			t.Fatalf("go list -deps -test %s: %v\n%s", pattern, err, out)
		}

		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			pkg, _, _ := strings.Cut(line, " ")
			pkg = strings.TrimSuffix(strings.TrimSuffix(pkg, ".test"), "_test")
			if within(pkg, repo) && !withinAny(pkg, openToPkg) {
				t.Errorf("%s reaches %s: of the repo, only pkg/ and platform/edge/contract are open to pkg", pattern, pkg)
			}
			if withinAny(pkg, closed) {
				t.Errorf("%s reaches %s: a vendor SDK, the Go SDK, and Pulumi outside providerkit/pulumi are never open to pkg", pattern, pkg)
			}
		}
	}
}

func withinAny(pkg string, roots []string) bool {
	for _, root := range roots {
		if within(pkg, root) {
			return true
		}
	}
	return false
}

func within(pkg, root string) bool {
	return pkg == root || strings.HasPrefix(pkg, root+"/")
}
