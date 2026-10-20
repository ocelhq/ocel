package account

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/envsourcesync"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/imageoptimizer"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/notfoundapi"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/taginvalidator"
)

func namesOf(t *testing.T, n Namespace) map[string]string {
	t.Helper()
	production, err := n.StackNameFor(environment.TierProduction)
	if err != nil {
		t.Fatalf("production stack name: %v", err)
	}
	preview, err := n.StackNameFor(environment.TierPreview)
	if err != nil {
		t.Fatalf("preview stack name: %v", err)
	}
	edgeUser, err := n.EdgeUserNameFor(environment.TierProduction)
	if err != nil {
		t.Fatalf("edge user name: %v", err)
	}
	edgeUserPreview, err := n.EdgeUserNameFor(environment.TierPreview)
	if err != nil {
		t.Fatalf("preview edge user name: %v", err)
	}
	originSecret, err := n.OriginSecretParamFor(environment.TierProduction)
	if err != nil {
		t.Fatalf("origin secret param: %v", err)
	}
	originSecretPreview, err := n.OriginSecretParamFor(environment.TierPreview)
	if err != nil {
		t.Fatalf("preview origin secret param: %v", err)
	}
	edgeParams, err := n.EdgeParamPrefix(environment.TierProduction, KindCloudflare)
	if err != nil {
		t.Fatalf("edge param prefix: %v", err)
	}
	edgeParamsPreview, err := n.EdgeParamPrefix(environment.TierPreview, KindCloudflare)
	if err != nil {
		t.Fatalf("preview edge param prefix: %v", err)
	}
	sync := envsourcesync.NamesFor(string(n), environment.TierProduction)
	syncPreview := envsourcesync.NamesFor(string(n), environment.TierPreview)
	return map[string]string{
		"core stack":             production,
		"preview core stack":     preview,
		"part stack":             n.PartStackName(CloudflareEdgePart, environment.TierProduction),
		"preview part stack":     n.PartStackName(CloudflareEdgePart, environment.TierPreview),
		"passphrase param":       n.PassphraseParamFor(environment.TierProduction),
		"edge user":              edgeUser,
		"preview edge user":      edgeUserPreview,
		"app boundary":           n.AppBoundaryNameFor(environment.TierProduction),
		"preview app boundary":   n.AppBoundaryNameFor(environment.TierPreview),
		"origin secret":          originSecret,
		"preview origin secret":  originSecretPreview,
		"edge param prefix":      edgeParams,
		"preview edge params":    edgeParamsPreview,
		"variables key alias":    n.variablesKeyAliasFor(environment.TierProduction),
		"edge invoke role":       n.EdgeInvokeRoleName(environment.TierProduction),
		"preview invoke role":    n.EdgeInvokeRoleName(environment.TierPreview),
		"not found api":          notfoundapi.NameFor(string(n), environment.TierProduction),
		"image optimizer":        imageoptimizer.NamesFor(string(n), environment.TierProduction).Function,
		"preview optimizer":      imageoptimizer.NamesFor(string(n), environment.TierPreview).Function,
		"tag invalidator":        taginvalidator.NamesFor(string(n), environment.TierPreview).Function,
		"invalidator queue":      taginvalidator.NamesFor(string(n), environment.TierProduction).Queue,
		"routes store":           n.EdgeRoutesStoreName(environment.TierProduction),
		"preview routes store":   n.EdgeRoutesStoreName(environment.TierPreview),
		"resolver":               n.EdgeResolverName(environment.TierPreview),
		"empty body function":    n.EdgeEmptyBodyName(environment.TierProduction),
		"cache policy":           n.EdgeCachePolicyName(environment.TierProduction),
		"headers policy":         n.EdgeHeadersPolicyName(environment.TierProduction),
		"asset access":           n.EdgeAssetAccessName(environment.TierProduction),
		"cache policy name":      n.PolicyName("edge-cache"),
		"app role path":          n.AppRolePathFor(environment.TierProduction),
		"env sync":               sync.Function,
		"env sync schedule role": sync.ScheduleRole,
		"schedule group":         sync.ScheduleGroup,
		"preview schedules":      syncPreview.ScheduleGroup,
		"schedule":               sync.Schedule,
		"preview schedule":       syncPreview.Schedule,
		"bastion":                bastion.NameFor(string(n), environment.TierProduction),
		"container cluster":      awsports.ContainerClusterName(string(n), environment.TierPreview),
		"public balancer":        awsports.PublicBalancerName(string(n), environment.TierPreview),
	}
}

func TestEveryOcelOwnedNameIsTheNamespaceFieldThenTheTierWord(t *testing.T) {
	want := map[string]string{
		"core stack":             "ocel-prod-bootstrap",
		"preview core stack":     "ocel-preview-bootstrap",
		"part stack":             "ocel-prod-bootstrap-cloudflare-edge",
		"preview part stack":     "ocel-preview-bootstrap-cloudflare-edge",
		"passphrase param":       "/ocel/pulumi/prod/passphrase",
		"edge user":              "ocel-prod-edge",
		"preview edge user":      "ocel-preview-edge",
		"app boundary":           "ocel-prod-boundary",
		"preview app boundary":   "ocel-preview-boundary",
		"origin secret":          "/ocel/origin/prod/secret",
		"preview origin secret":  "/ocel/origin/preview/secret",
		"edge param prefix":      "/ocel/edge/prod/cloudflare",
		"preview edge params":    "/ocel/edge/preview/cloudflare",
		"variables key alias":    "alias/ocel-prod-variables",
		"edge invoke role":       "ocel-prod-edge-invoke",
		"preview invoke role":    "ocel-preview-edge-invoke",
		"not found api":          "ocel-prod-not-found",
		"image optimizer":        "ocel-prod-optimizer",
		"preview optimizer":      "ocel-preview-optimizer",
		"tag invalidator":        "ocel-preview-invalidator",
		"invalidator queue":      "ocel-prod-invalidator",
		"routes store":           "ocel-prod-routes",
		"preview routes store":   "ocel-preview-routes",
		"resolver":               "ocel-preview-resolver",
		"empty body function":    "ocel-prod-empty-body",
		"cache policy":           "ocel-prod-cache",
		"headers policy":         "ocel-prod-headers",
		"asset access":           "ocel-prod-assets",
		"cache policy name":      "ocel-edge-cache",
		"app role path":          "/ocel/prod/app/",
		"env sync":               "ocel-prod-env-sync",
		"env sync schedule role": "ocel-prod-env-sync-schedule",
		"schedule group":         "ocel-prod-env-sync",
		"preview schedules":      "ocel-preview-env-sync",
		"schedule":               "ocel-prod-env-sync",
		"preview schedule":       "ocel-preview-env-sync",
		"bastion":                "ocel-prod-bastion",
		"container cluster":      "ocel-preview-containers",
		"public balancer":        "ocel-preview-public",
	}
	got := namesOf(t, defaultNamespace)
	for what := range want {
		if _, named := got[what]; !named {
			t.Errorf("the test wants the %s, which namesOf never names", what)
		}
	}
	for what, got := range got {
		if want[what] != got {
			t.Errorf("the %s is %q under the default namespace, want %q", what, got, want[what])
		}
	}
}

func TestEveryNameContainsTheNamespace(t *testing.T) {
	for what, got := range namesOf(t, Namespace("j-abc-cache")) {
		if strings.Contains(got, "ocel") {
			t.Errorf("the %s is %q, which still names ocel rather than the namespace it was derived from", what, got)
		}
		if !strings.Contains(got, "j-abc-cache") {
			t.Errorf("the %s is %q and does not contain the namespace it was derived from", what, got)
		}
	}
}

func TestALongNamespaceIsNamedByItsTwelveCharacterField(t *testing.T) {
	long := Namespace("j-1874-deploy-next-cloudflare")
	for what, got := range namesOf(t, long) {
		if strings.Contains(got, string(long)) {
			t.Errorf("the %s is %q, which spells out the whole namespace rather than its field", what, got)
		}
		if !strings.Contains(got, "j-187-") {
			t.Errorf("the %s is %q and does not contain the namespace's field", what, got)
		}
	}
	if got, want := namesOf(t, long)["core stack"], naming.NamespaceField(string(long))+"-prod-bootstrap"; got != want {
		t.Errorf("core stack = %q, want %q", got, want)
	}
}

func TestEveryNameFitsAWSAtTheLongestNamespace(t *testing.T) {
	limits := map[string]int{
		"core stack":             128,
		"preview core stack":     128,
		"part stack":             128,
		"preview part stack":     128,
		"passphrase param":       2048,
		"edge user":              64,
		"preview edge user":      64,
		"app boundary":           128,
		"preview app boundary":   128,
		"origin secret":          2048,
		"preview origin secret":  2048,
		"edge param prefix":      2048,
		"preview edge params":    2048,
		"variables key alias":    256,
		"edge invoke role":       64,
		"preview invoke role":    64,
		"not found api":          128,
		"image optimizer":        64,
		"preview optimizer":      64,
		"tag invalidator":        64,
		"invalidator queue":      80,
		"routes store":           64,
		"preview routes store":   64,
		"resolver":               64,
		"empty body function":    64,
		"cache policy":           128,
		"headers policy":         128,
		"asset access":           64,
		"cache policy name":      128,
		"app role path":          512,
		"env sync":               64,
		"env sync schedule role": 64,
		"schedule group":         64,
		"preview schedules":      64,
		"schedule":               64,
		"preview schedule":       64,
		"bastion":                64,
		"container cluster":      32,
		"public balancer":        32,
	}

	longest := Namespace(strings.Repeat("a", provider.MaxNamespaceLength))
	for what, got := range namesOf(t, longest) {
		limit, known := limits[what]
		if !known {
			t.Fatalf("the %s has no AWS length limit recorded, so the bound cannot be trusted", what)
		}
		if len(got) > limit {
			t.Errorf("at the longest namespace the %s is %d characters, and AWS allows %d", what, len(got), limit)
		}
	}
}

func TestNoOcelOwnedNameFallsUnderTheAppFence(t *testing.T) {
	for _, n := range []Namespace{defaultNamespace, "j-abc-cache", Namespace(strings.Repeat("a", provider.MaxNamespaceLength))} {
		fence := naming.AppFence(string(n))
		for what, got := range namesOf(t, n) {
			if strings.HasPrefix(strings.TrimPrefix(got, "alias/"), fence) {
				t.Errorf("the %s is %q, under the fence %q the deploy credential reaches every app resource by", what, got, fence)
			}
		}
	}
}
