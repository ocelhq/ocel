package bootstrap

import (
	"strings"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func namesOf(t *testing.T, n Namespace) map[string]string {
	t.Helper()
	production, err := n.StackNameFor(ClassProduction)
	if err != nil {
		t.Fatalf("production stack name: %v", err)
	}
	preview, err := n.StackNameFor(ClassPreview)
	if err != nil {
		t.Fatalf("preview stack name: %v", err)
	}
	edgeUser, err := n.EdgeUserNameFor(ClassProduction)
	if err != nil {
		t.Fatalf("edge user name: %v", err)
	}
	edgeUserPreview, err := n.EdgeUserNameFor(ClassPreview)
	if err != nil {
		t.Fatalf("preview edge user name: %v", err)
	}
	originSecret, err := n.OriginSecretParamFor(ClassProduction)
	if err != nil {
		t.Fatalf("origin secret param: %v", err)
	}
	originSecretPreview, err := n.OriginSecretParamFor(ClassPreview)
	if err != nil {
		t.Fatalf("preview origin secret param: %v", err)
	}
	edgeParams, err := n.EdgeParamPrefix(ClassProduction, KindCloudflare)
	if err != nil {
		t.Fatalf("edge param prefix: %v", err)
	}
	edgeParamsPreview, err := n.EdgeParamPrefix(ClassPreview, KindCloudflare)
	if err != nil {
		t.Fatalf("preview edge param prefix: %v", err)
	}
	queue, dlq := n.revalidateQueueNames(ClassProduction)
	previewQueue, previewDLQ := n.revalidateQueueNames(ClassPreview)
	return map[string]string{
		"core stack":            production,
		"preview core stack":    preview,
		"feature stack":         n.FeatureStackName(FeatureImageOptimization, ClassProduction),
		"preview feature stack": n.FeatureStackName(FeatureImageOptimization, ClassPreview),
		"passphrase param":      n.PassphraseParamName(),
		"edge user":             edgeUser,
		"preview edge user":     edgeUserPreview,
		"app boundary":          n.AppBoundaryNameFor(ClassProduction),
		"preview app boundary":  n.AppBoundaryNameFor(ClassPreview),
		"origin secret":         originSecret,
		"preview origin secret": originSecretPreview,
		"edge param prefix":     edgeParams,
		"preview edge params":   edgeParamsPreview,
		"vars key alias":        n.varsKeyAliasFor(ClassProduction),
		"edge invoke role":      n.EdgeInvokeRoleName(edge.ClassProduction),
		"preview invoke role":   n.EdgeInvokeRoleName(edge.ClassPreview),
		"not found api":         n.EdgeNotFoundAPIName(edge.ClassProduction),
		"routes store":          n.EdgeRoutesStoreName(edge.ClassProduction),
		"preview routes store":  n.EdgeRoutesStoreName(edge.ClassPreview),
		"resolver":              n.EdgeResolverName(edge.ClassPreview),
		"cache policy":          n.edgeCachePolicyName(edge.ClassProduction),
		"headers policy":        n.edgeHeadersPolicyName(edge.ClassProduction),
		"asset access":          n.edgeAssetAccessName(edge.ClassProduction),
		"revalidate queue":      queue,
		"revalidate dlq":        dlq,
		"preview queue":         previewQueue,
		"preview dlq":           previewDLQ,
		"cache policy name":     n.policyName("edge-cache"),
	}
}

func TestDefaultNamespaceKeepsEveryNameAsItStands(t *testing.T) {
	want := map[string]string{
		"core stack":            "ocel-bootstrap",
		"preview core stack":    "ocel-bootstrap-preview",
		"feature stack":         "ocel-bootstrap-image-optimization",
		"preview feature stack": "ocel-bootstrap-image-optimization-preview",
		"passphrase param":      "/ocel/pulumi/passphrase",
		"edge user":             "ocel-edge",
		"preview edge user":     "ocel-edge-preview",
		"app boundary":          "ocel-app-boundary",
		"preview app boundary":  "ocel-app-boundary-preview",
		"origin secret":         "/ocel/origin/secret",
		"preview origin secret": "/ocel/origin/secret-preview",
		"edge param prefix":     "/ocel/edge/cloudflare",
		"preview edge params":   "/ocel/edge/cloudflare-preview",
		"vars key alias":        "alias/ocel-vars-production",
		"edge invoke role":      "ocel-edge-invoke",
		"preview invoke role":   "ocel-edge-invoke-preview",
		"not found api":         "ocel-not-found-production",
		"routes store":          "ocel-routes",
		"preview routes store":  "ocel-routes-preview",
		"resolver":              "ocel-resolver-preview",
		"cache policy":          "ocel-cache",
		"headers policy":        "ocel-headers",
		"asset access":          "ocel-assets",
		"revalidate queue":      "ocel-revalidate.fifo",
		"revalidate dlq":        "ocel-revalidate-dlq.fifo",
		"preview queue":         "ocel-revalidate-preview.fifo",
		"preview dlq":           "ocel-revalidate-preview-dlq.fifo",
		"cache policy name":     "ocel-edge-cache",
	}
	for what, got := range namesOf(t, DefaultNamespace) {
		if want[what] != got {
			t.Errorf("the %s is %q under the default namespace, want %q", what, got, want[what])
		}
	}
}

func TestEveryNameCarriesTheNamespace(t *testing.T) {
	for what, got := range namesOf(t, Namespace("j-abc-cache")) {
		if strings.Contains(got, "ocel") {
			t.Errorf("the %s is %q, which still names ocel rather than the namespace it was derived from", what, got)
		}
		if !strings.Contains(got, "j-abc-cache") {
			t.Errorf("the %s is %q and does not carry the namespace it was derived from", what, got)
		}
	}
}

func TestTheLengthBoundIsTheTightestAWSAllows(t *testing.T) {
	limits := map[string]int{
		"core stack":            128,
		"preview core stack":    128,
		"feature stack":         128,
		"preview feature stack": 128,
		"passphrase param":      2048,
		"edge user":             64,
		"preview edge user":     64,
		"app boundary":          128,
		"preview app boundary":  128,
		"origin secret":         2048,
		"preview origin secret": 2048,
		"edge param prefix":     2048,
		"preview edge params":   2048,
		"vars key alias":        256,
		"edge invoke role":      64,
		"preview invoke role":   64,
		"not found api":         128,
		"routes store":          64,
		"preview routes store":  64,
		"resolver":              64,
		"cache policy":          128,
		"headers policy":        128,
		"asset access":          64,
		"revalidate queue":      80,
		"revalidate dlq":        80,
		"preview queue":         80,
		"preview dlq":           80,
		"cache policy name":     128,
	}

	longest := Namespace(strings.Repeat("a", MaxNamespaceLength))
	tight := false
	for what, got := range namesOf(t, longest) {
		limit, known := limits[what]
		if !known {
			t.Fatalf("the %s has no AWS length limit recorded, so the bound cannot be trusted", what)
		}
		if len(got) > limit {
			t.Errorf("at the longest namespace the %s is %d characters, and AWS allows %d", what, len(got), limit)
		}
		if len(got) == limit {
			tight = true
		}
	}
	if !tight {
		t.Errorf("no name reaches its AWS limit at a namespace of %d characters, so the bound is shorter than it needs to be", MaxNamespaceLength)
	}
}
