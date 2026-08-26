package vps_test

import (
	"os/exec"
	"strings"
	"testing"
)

var reachable = map[string]bool{
	"github.com/ocelhq/ocel/platform/vps/provider":  true,
	"github.com/ocelhq/ocel/pkg/providerkit":        true,
	"github.com/ocelhq/ocel/pkg/channel":            true,
	"github.com/ocelhq/ocel/pkg/naming":             true,
	"github.com/ocelhq/ocel/pkg/proto":              true,
	"github.com/ocelhq/ocel/platform/edge/contract": true,
}

var forbidden = []string{
	"github.com/ocelhq/ocel/pkg/providerkit/pulumi",
	"golang.org/x/crypto/ssh",
	"github.com/gliderlabs/ssh",
	"github.com/melbahja/goph",
	"github.com/pkg/sftp",
	"github.com/aws/aws-sdk-go",
	"github.com/pulumi/",
	"github.com/cloudflare/",
	"cloud.google.com/go",
}

func TestTheProviderReachesNoCloudAndNoGoSSHStack(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "list", "-deps", "./...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}

	for _, pkg := range strings.Fields(string(out)) {
		if module, ok := ocelModuleOf(pkg); ok && !reachable[module] {
			t.Errorf("the vps provider reaches %s, which the module's dependency list does not allow", pkg)
		}
		for _, engine := range forbidden {
			if strings.HasPrefix(pkg, engine) {
				t.Errorf("the vps provider reaches %s: it speaks to hosts through the OpenSSH binaries, so ssh_config and known_hosts stay the user's own", pkg)
			}
		}
	}
}

func ocelModuleOf(pkg string) (string, bool) {
	const repo = "github.com/ocelhq/ocel/"
	if !strings.HasPrefix(pkg, repo) {
		return "", false
	}
	for module := range reachable {
		if pkg == module || strings.HasPrefix(pkg, module+"/") {
			return module, true
		}
	}
	return pkg, true
}
