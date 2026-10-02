package vps_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const (
	repo       = "github.com/ocelhq/ocel/"
	dnsRecords = repo + "platform/edge/cloudflare/deploy"
	s3Store    = repo + "platform/s3"
)

var carriers = []string{dnsRecords, s3Store}

var reachable = map[string]bool{
	"github.com/ocelhq/ocel/platform/vps/provider":   true,
	"github.com/ocelhq/ocel/pkg/provider":            true,
	"github.com/ocelhq/ocel/pkg/arch":                true,
	"github.com/ocelhq/ocel/pkg/buildoutput":         true,
	"github.com/ocelhq/ocel/pkg/configdoc":           true,
	"github.com/ocelhq/ocel/pkg/containerimage":      true,
	"github.com/ocelhq/ocel/pkg/cron":                true,
	"github.com/ocelhq/ocel/pkg/dotenv":              true,
	"github.com/ocelhq/ocel/pkg/edge":                true,
	"github.com/ocelhq/ocel/pkg/environment":         true,
	"github.com/ocelhq/ocel/pkg/envsource":           true,
	"github.com/ocelhq/ocel/pkg/variablestore":       true,
	"github.com/ocelhq/ocel/pkg/variablestoreserver": true,
	"github.com/ocelhq/ocel/pkg/images":              true,
	"github.com/ocelhq/ocel/pkg/naming":              true,
	"github.com/ocelhq/ocel/pkg/pricing":             true,
	"github.com/ocelhq/ocel/pkg/processenv":          true,
	"github.com/ocelhq/ocel/pkg/progress":            true,
	"github.com/ocelhq/ocel/pkg/progressproto":       true,
	"github.com/ocelhq/ocel/pkg/proto":               true,
	"github.com/ocelhq/ocel/pkg/keyvalue":            true,
	"github.com/ocelhq/ocel/pkg/kvstore":             true,
	"github.com/ocelhq/ocel/pkg/localrpc":            true,
	"github.com/ocelhq/ocel/pkg/realtime":            true,
	"github.com/ocelhq/ocel/pkg/refusal":             true,
	"github.com/ocelhq/ocel/pkg/router":              true,
	"github.com/ocelhq/ocel/pkg/runtime":             true,
	"github.com/ocelhq/ocel/pkg/seal":                true,
	"github.com/ocelhq/ocel/pkg/stackrecords":        true,
	"github.com/ocelhq/ocel/pkg/statedir":            true,
	"github.com/ocelhq/ocel/pkg/target":              true,
	dnsRecords:                                       true,
	s3Store:                                          true,
}

var goSSHStack = []string{
	"golang.org/x/crypto/ssh",
	"github.com/gliderlabs/ssh",
	"github.com/melbahja/goph",
	"github.com/pkg/sftp",
}

var provisioningEngines = []string{
	"github.com/ocelhq/ocel/pkg/provider/pulumi",
	"github.com/aws/aws-sdk-go",
	"github.com/pulumi/",
	"github.com/cloudflare/",
	"cloud.google.com/go",
}

func TestTheProviderReachesNoCloudAndNoGoSSHStackOfItsOwn(t *testing.T) {
	t.Parallel()

	var carried []string
	for _, carrier := range carriers {
		carried = append(carried, depsOf(t, carrier)...)
	}
	own := slices.DeleteFunc(depsOf(t, "./..."), func(pkg string) bool {
		return !strings.HasPrefix(pkg, repo) && slices.Contains(carried, pkg)
	})

	for _, pkg := range own {
		if module, ok := ocelModuleOf(pkg); ok && !reachable[module] {
			t.Errorf("the vps provider reaches %s, which the module's dependency list does not allow", pkg)
		}
		for _, client := range goSSHStack {
			if strings.HasPrefix(pkg, client) {
				t.Errorf("the vps provider reaches %s: it speaks to hosts through the OpenSSH binaries, so ssh_config and known_hosts stay the user's own", pkg)
			}
		}
		for _, engine := range provisioningEngines {
			if strings.HasPrefix(pkg, engine) {
				t.Errorf("the vps provider reaches %s of its own: a box is provisioned over a shell session, and the only vendor SDKs it binds are the ones %s bring with them", pkg, strings.Join(carriers, " and "))
			}
		}
	}
}

func TestTheProviderNamesNoVendorSDKInItsOwnImports(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "list", "-f", "{{$path := .ImportPath}}{{range .Imports}}{{$path}} {{.}}\n{{end}}", "./...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}

	for line := range strings.Lines(string(out)) {
		mine, imported, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		for _, engine := range append(slices.Clone(provisioningEngines), goSSHStack...) {
			if strings.HasPrefix(imported, engine) {
				t.Errorf("%s imports %s itself; a vendor SDK reaches this provider only behind %s", mine, imported, strings.Join(carriers, " or "))
			}
		}
	}
}

func depsOf(t *testing.T, pattern string) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", pattern).CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps %s: %v\n%s", pattern, err, out)
	}
	return strings.Fields(string(out))
}

func ocelModuleOf(pkg string) (string, bool) {
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
