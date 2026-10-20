package account

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
)

func hasEdgeUser(t *testing.T, template string) bool {
	t.Helper()
	var tmpl struct {
		Resources map[string]struct {
			Type string `json:"Type"`
		} `json:"Resources"`
	}
	if err := json.Unmarshal([]byte(template), &tmpl); err != nil {
		t.Fatalf("provisioned template is not valid JSON: %v", err)
	}
	for _, r := range tmpl.Resources {
		if r.Type == "AWS::IAM::User" {
			return true
		}
	}
	return false
}

func assertMintedSecrets(t *testing.T, ssmc *fakeSSM, names ...string) {
	t.Helper()
	for _, name := range names {
		value, ok := ssmc.params[name]
		if !ok {
			t.Errorf("bootstrap left %s empty; every deploy that reads it refuses", name)
			continue
		}
		if _, err := hex.DecodeString(value); err != nil || len(value) != 64 {
			t.Errorf("%s = %q, want 32 random bytes", name, value)
		}
	}
}

func assertMintedOriginSecret(t *testing.T, ssmc *fakeSSM, name string) {
	t.Helper()
	value, ok := ssmc.params[name]
	if !ok {
		t.Errorf("bootstrap left %s empty; every deploy that reads it refuses", name)
		return
	}
	minted, err := OriginSecretOf(value)
	if err != nil {
		t.Errorf("%s = %q, want the origin secret record: %v", name, value, err)
		return
	}
	if _, err := hex.DecodeString(minted.Current); err != nil || len(minted.Current) != 64 {
		t.Errorf("%s stores %q, want 32 random bytes", name, minted.Current)
	}
	if minted.CreatedAt.IsZero() {
		t.Errorf("%s records no mint time, so nothing can age it into a rotation", name)
	}
}

func apisOf(stacks *fakeCFN, ssmc *fakeSSM, iamc *fakeIAM, store ObjectStore) APIs {
	return apisFronting(stacks, ssmc, iamc, store, currentEdge())
}

func apisFronting(stacks *fakeCFN, ssmc *fakeSSM, iamc *fakeIAM, store ObjectStore, front edge.Edge) APIs {
	return apisAcross(stacks, ssmc, iamc, store, front, newFakeEdges(front))
}

func apisAcross(stacks *fakeCFN, ssmc *fakeSSM, iamc *fakeIAM, store ObjectStore, front edge.Edge, registry *fakeEdges) APIs {
	stacks.users = iamc
	return APIs{CFN: stacks, SSM: ssmc, IAM: iamc, Layers: newFakeLayers(), Store: store, Records: newFakeRecords().in, Edge: front, Edges: registry}
}

func everything() Request { return Request{Parts: partNames()} }

func edgeStack(tier environment.Tier) string {
	return defaultNamespace.PartStackName(CloudflareEdgePart, tier)
}
func keyStack(tier environment.Tier) string {
	return defaultNamespace.PartStackName(provider.VariablesKeyPart, tier)
}

func TestRun(t *testing.T) {
	t.Run("core alone provisions no part stack", func(t *testing.T) {
		stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		ed := &fakeEdge{}
		frontedBy(t, ed)

		if err := Run(context.Background(), apisOf(stacks, ssmc, iamc, preloadedStore()), defaultNamespace, environment.TierProduction, Request{}, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		want := []string{coreStackName}
		if got := stacks.stacks(); !slices.Equal(got, want) {
			t.Errorf("stacks = %v, want %v", got, want)
		}
		if ed.bootstraps != 1 {
			t.Errorf("the front was bootstrapped %d times for a core-only bootstrap, want 1: the edge is the front, not a part", ed.bootstraps)
		}
		if len(iamc.created) != 0 {
			t.Errorf("minted %v for a core-only bootstrap", iamc.created)
		}
	})

	t.Run("asking for the cloudflare edge provisions its stack beside the core alone", func(t *testing.T) {
		stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		err := Run(context.Background(), apisOf(stacks, ssmc, iamc, preloadedStore()), defaultNamespace, environment.TierProduction,
			Request{Parts: []string{CloudflareEdgePart}}, nil)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		want := []string{coreStackName, edgeStack(environment.TierProduction)}
		slices.Sort(want)
		if got := stacks.stacks(); !slices.Equal(got, want) {
			t.Errorf("stacks = %v, want %v", got, want)
		}
	})

	t.Run("the core is applied before every part stack", func(t *testing.T) {
		stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		if err := Run(context.Background(), apisOf(stacks, ssmc, iamc, preloadedStore()), defaultNamespace, environment.TierProduction, everything(), nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		core := stacks.firstApplied(coreStackName)
		for _, name := range []string{edgeStack(environment.TierProduction), keyStack(environment.TierProduction)} {
			part := stacks.firstApplied(name)
			if core < 0 || part < 0 {
				t.Fatalf("applied %v, want the core and %s among them", stacks.applied, name)
			}
			if core >= part {
				t.Errorf("applied in order %v, want the core before %s", stacks.applied, name)
			}
		}
	})

	t.Run("no stack reaches across stacks with an export", func(t *testing.T) {
		stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		if err := Run(context.Background(), apisOf(stacks, ssmc, iamc, preloadedStore()), defaultNamespace, environment.TierProduction, everything(), nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		for _, name := range stacks.stacks() {
			if body := stacks.template(name); strings.Contains(body, "ImportValue") || strings.Contains(body, `"Export"`) {
				t.Errorf("%s reaches across stacks with a CloudFormation export", name)
			}
		}
	})

	t.Run("the cloudflare edge part provisions the edge reader", func(t *testing.T) {
		stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		ed := &fakeEdge{kind: "cloudflare"}
		frontedBy(t, ed)

		if err := Run(context.Background(), apisOf(stacks, ssmc, iamc, preloadedStore()), defaultNamespace, environment.TierProduction, everything(), nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if ed.bootstraps != 1 {
			t.Errorf("edge bootstrapped %d times, want exactly 1", ed.bootstraps)
		}
		if !hasEdgeUser(t, stacks.template(edgeStack(environment.TierProduction))) {
			t.Error("the cloudflare edge part did not provision the edge reader IAM user")
		}
		if hasEdgeUser(t, stacks.template(coreStackName)) {
			t.Error("core provisioned an edge reader; only the edge that needs one may")
		}
		if len(iamc.created) != 1 || iamc.created[0] != edgeUserName {
			t.Errorf("minted keys for %v, want [%s]", iamc.created, edgeUserName)
		}
		if _, ok := ssmc.params[cloudflareNames(environment.TierProduction).credentialsParam]; !ok {
			t.Errorf("no static key stored at %s", cloudflareNames(environment.TierProduction).credentialsParam)
		}
	})

	t.Run("mints the seed every release's purge secret is derived from", func(t *testing.T) {
		ssmc := newFakeSSM()
		frontedBy(t, &fakeEdge{
			kind: "cloudflare",
			out:  edge.BootstrapOutput{ReleasesStore: new(offeredReleasesStore())},
		})

		if err := Run(context.Background(), apisOf(newFakeCFN(), ssmc, &fakeIAM{}, preloadedStore()), defaultNamespace, environment.TierProduction, everything(), nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		assertMintedSecrets(t, ssmc, cloudflareNames(environment.TierProduction).purgeSeedParam)
		assertMintedOriginSecret(t, ssmc, originSecretParam)
	})

	t.Run("bootstraps the edge for its own tier", func(t *testing.T) {
		for _, want := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
			t.Run(string(want), func(t *testing.T) {
				ed := &fakeEdge{kind: "cloudflare"}
				frontedBy(t, ed)
				if err := Run(context.Background(), apisOf(newFakeCFN(), newFakeSSM(), &fakeIAM{}, preloadedStore()), defaultNamespace, want, everything(), nil); err != nil {
					t.Fatalf("run: %v", err)
				}
				if ed.tier != want {
					t.Errorf("edge bootstrapped for tier %q, want %q", ed.tier, want)
				}
			})
		}
	})

	t.Run("no cloudflare edge leaves no credential", func(t *testing.T) {
		for _, tc := range []struct {
			tier      environment.Tier
			credParam string
		}{
			{environment.TierProduction, cloudflareNames(environment.TierProduction).credentialsParam},
			{environment.TierPreview, cloudflareNames(environment.TierPreview).credentialsParam},
		} {
			t.Run(string(tc.tier), func(t *testing.T) {
				stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
				frontedBy(t, &fakeEdge{})

				req := Request{Parts: []string{provider.VariablesKeyPart}}
				if err := Run(context.Background(), apisOf(stacks, ssmc, iamc, preloadedStore()), defaultNamespace, tc.tier, req, nil); err != nil {
					t.Fatalf("run: %v", err)
				}
				for _, name := range stacks.stacks() {
					if hasEdgeUser(t, stacks.template(name)) {
						t.Errorf("%s provisioned an IAM user with no external edge asked for", name)
					}
				}
				if len(iamc.created) != 0 {
					t.Errorf("minted static keys for %v with no external edge asked for", iamc.created)
				}
				if _, ok := ssmc.params[tc.credParam]; ok {
					t.Errorf("stored a static key at %s with no external edge asked for", tc.credParam)
				}
			})
		}
	})

	t.Run("adopts nothing from the worker client certificate offer and warns nothing", func(t *testing.T) {
		stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare", out: edge.BootstrapOutput{
			ClientCertificate: &edge.ClientCertificate{ID: "certificate-1", Authorities: "-----BEGIN CERTIFICATE-----"},
			ReleasesStore:     new(offeredReleasesStore()),
		}})
		log := &warningLog{Log: progress.Discard()}

		if err := Run(context.Background(), apisOf(stacks, ssmc, iamc, preloadedStore()), defaultNamespace, environment.TierProduction, everything(), log); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(log.warnings) != 0 {
			t.Errorf("warnings = %v, want none for an offer this bootstrap deliberately adopts nothing from", log.warnings)
		}
		for name, value := range ssmc.params {
			if strings.Contains(fmt.Sprint(value), "certificate-1") {
				t.Errorf("stored the worker client certificate offer in %s", name)
			}
		}
		if _, ok := ssmc.params[cloudflareNames(environment.TierProduction).releasesStoreParam]; !ok {
			t.Errorf("the releases store offer alongside it was not adopted")
		}
	})

	t.Run("no offers stores no releases store and mints no seed", func(t *testing.T) {
		stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		if err := Run(context.Background(), apisOf(stacks, ssmc, iamc, preloadedStore()), defaultNamespace, environment.TierProduction, everything(), nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		names := cloudflareNames(environment.TierProduction)
		for _, param := range []string{names.releasesStoreParam, names.purgeSeedParam} {
			if _, ok := ssmc.params[param]; ok {
				t.Errorf("stored %s for an edge that offered no releases store", param)
			}
		}
	})

	t.Run("adopts the releases store per tier", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			tier  environment.Tier
			param string
		}{
			{"production", environment.TierProduction, cloudflareNames(environment.TierProduction).releasesStoreParam},
			{"preview", environment.TierPreview, cloudflareNames(environment.TierPreview).releasesStoreParam},
		} {
			t.Run(tc.name, func(t *testing.T) {
				ssmc := newFakeSSM()
				frontedBy(t, &fakeEdge{kind: "cloudflare", out: edge.BootstrapOutput{
					ReleasesStore: new(offeredReleasesStore()),
				}})

				if err := Run(context.Background(), apisOf(newFakeCFN(), ssmc, &fakeIAM{}, preloadedStore()), defaultNamespace, tc.tier, everything(), nil); err != nil {
					t.Fatalf("run: %v", err)
				}
				if _, ok := ssmc.params[tc.param]; !ok {
					t.Fatalf("no releases store stored at %s", tc.param)
				}
				got, err := ReadReleasesStoreFor(context.Background(), ssmc, defaultNamespace, tc.tier, KindCloudflare)
				if err != nil {
					t.Fatalf("ReadReleasesStoreFor: %v", err)
				}
				if got.ScriptName != "ocel-releases-store" || got.BootstrapCredential != "cred-prod" {
					t.Errorf("stored store = %+v, want the offered coordinates", got)
				}
			})
		}
	})

	t.Run("a dangling releases store credential fails bootstrap", func(t *testing.T) {
		stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		offer := offeredReleasesStore()
		offer.BootstrapCredential = ""
		frontedBy(t, &fakeEdge{kind: "cloudflare", out: edge.BootstrapOutput{ReleasesStore: &offer}})

		err := Run(context.Background(), apisOf(stacks, ssmc, iamc, preloadedStore()), defaultNamespace, environment.TierProduction, everything(), nil)
		if err == nil {
			t.Fatal("expected Run to fail on an unrecoverable releases-store credential")
		}
		names := cloudflareNames(environment.TierProduction)
		for _, param := range []string{names.releasesStoreParam, names.purgeSeedParam} {
			if _, ok := ssmc.params[param]; ok {
				t.Errorf("stored %s despite the hazard", param)
			}
		}
		if slices.Contains(stacks.stacks(), edgeStack(environment.TierProduction)) {
			t.Error("provisioned the edge stack despite the hazard")
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		for i := range 2 {
			if err := Run(context.Background(), apisOf(stacks, ssmc, iamc, preloadedStore()), defaultNamespace, environment.TierProduction, everything(), nil); err != nil {
				t.Fatalf("Run %d: %v", i+1, err)
			}
		}
		if len(iamc.created) != 1 {
			t.Errorf("minted %d keys across two bootstraps, want 1: %v", len(iamc.created), iamc.created)
		}
		var creds EdgeCredentials
		if err := json.Unmarshal([]byte(ssmc.params[cloudflareNames(environment.TierProduction).credentialsParam]), &creds); err != nil {
			t.Fatalf("stored credentials are not readable after a re-run: %v", err)
		}
		if creds.AccessKeyID != "AKIAEDGE" {
			t.Errorf("stored key = %q, want the first minted key", creds.AccessKeyID)
		}
		if want := 1 + len(partNames()); stacks.creates != want {
			t.Errorf("stacks were created %d times across two bootstraps, want one create each for core and its %d parts", stacks.creates, len(partNames()))
		}
		if want := 1 + len(partNames()); stacks.noops != want {
			t.Errorf("the second bootstrap submitted %d unchanged templates, want %d: a re-run must converge, not re-provision", stacks.noops, want)
		}
	})

	t.Run("edge bootstrap failure stops provisioning the part it belongs to", func(t *testing.T) {
		stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare", err: errors.New("edge API unreachable")})

		err := Run(context.Background(), apisOf(stacks, ssmc, iamc, preloadedStore()), defaultNamespace, environment.TierProduction, everything(), nil)
		if err == nil {
			t.Fatal("expected Run to fail when the edge bootstrap fails")
		}
		if slices.Contains(stacks.stacks(), edgeStack(environment.TierProduction)) {
			t.Error("provisioned the edge stack despite a failed edge bootstrap")
		}
		if len(iamc.created) != 0 {
			t.Errorf("minted %v despite a failed edge bootstrap", iamc.created)
		}
	})

	t.Run("dropped names take their stacks with them and leave the core", func(t *testing.T) {
		stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})
		apis := apisOf(stacks, ssmc, iamc, preloadedStore())

		if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, everything(), nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		dropped := Request{Remove: []string{provider.VariablesKeyPart, CloudflareEdgePart}}
		if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, dropped, nil); err != nil {
			t.Fatalf("Run without the parts: %v", err)
		}
		want := []string{keyStack(environment.TierProduction), edgeStack(environment.TierProduction)}
		if !slices.Equal(stacks.deleted, want) {
			t.Errorf("deleted %v, want %v", stacks.deleted, want)
		}
		if got := stacks.stacks(); !slices.Equal(got, []string{coreStackName}) {
			t.Errorf("stacks left = %v, want the core alone", got)
		}
	})
}

func TestRunPreview(t *testing.T) {
	t.Run("mints the preview seed every release's purge secret is derived from", func(t *testing.T) {
		ssmc := newFakeSSM()
		frontedBy(t, &fakeEdge{kind: "cloudflare", out: edge.BootstrapOutput{
			ReleasesStore: new(offeredReleasesStore()),
		}})

		if err := Run(context.Background(), apisOf(newFakeCFN(), ssmc, &fakeIAM{}, preloadedStore()), defaultNamespace, environment.TierPreview, everything(), nil); err != nil {
			t.Fatalf("RunPreview: %v", err)
		}
		assertMintedSecrets(t, ssmc, cloudflareNames(environment.TierPreview).purgeSeedParam)
		assertMintedOriginSecret(t, ssmc, previewOriginSecret)
		for _, name := range []string{originSecretParam, cloudflareNames(environment.TierProduction).purgeSeedParam} {
			if _, ok := ssmc.params[name]; ok {
				t.Errorf("a preview bootstrap wrote %s, want the production secrets left alone", name)
			}
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		var passphrase string
		for i := range 2 {
			if err := Run(context.Background(), apisOf(stacks, ssmc, iamc, preloadedStore()), defaultNamespace, environment.TierPreview, everything(), nil); err != nil {
				t.Fatalf("RunPreview %d: %v", i+1, err)
			}
			if i == 0 {
				passphrase = ssmc.params[passphraseParam]
			}
		}

		if want := 1 + len(partNames()); stacks.creates != want {
			t.Errorf("stacks were created %d times across two preview bootstraps, want one create each for core and its %d parts", stacks.creates, len(partNames()))
		}
		if want := 1 + len(partNames()); stacks.noops != want {
			t.Errorf("the second bootstrap submitted %d unchanged templates, want %d: a re-run must converge, not re-provision", stacks.noops, want)
		}
		for _, name := range stacks.stacks() {
			if !strings.HasSuffix(name, "-preview") {
				t.Errorf("a preview bootstrap touched %s, want only preview-suffixed stacks", name)
			}
		}

		if len(iamc.created) != 1 {
			t.Errorf("minted %d keys across two preview bootstraps, want 1: %v", len(iamc.created), iamc.created)
		}
		var creds EdgeCredentials
		if err := json.Unmarshal([]byte(ssmc.params[cloudflareNames(environment.TierPreview).credentialsParam]), &creds); err != nil {
			t.Fatalf("stored preview credentials are not readable after a re-run: %v", err)
		}
		if creds.AccessKeyID != "AKIAEDGE" {
			t.Errorf("stored key = %q, want the first minted key", creds.AccessKeyID)
		}
		if got := ssmc.params[passphraseParam]; got != passphrase {
			t.Error("the second bootstrap regenerated the Pulumi passphrase, orphaning every preview stack encrypted under the first")
		}

		tmpl := parseVariablesTemplate(t, stacks.template(previewStackName))
		if _, ok := tmpl.Resources["VariablesTable"]; !ok {
			t.Error("the preview stack no longer declares VariablesTable after a re-run")
		}
	})
}

func TestRunDefaultEdge(t *testing.T) {
	t.Run("core bootstraps the edge the provider fronts with by default", func(t *testing.T) {
		front := &fakeEdge{kind: "cloudfront"}
		stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		apis := apisFronting(stacks, ssmc, iamc, preloadedStore(), front)
		if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, Request{}, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if front.bootstraps != 1 {
			t.Errorf("the default edge was bootstrapped %d times, want 1: nothing else provisions its shared resources", front.bootstraps)
		}
		if front.tier != environment.TierProduction {
			t.Errorf("the default edge was bootstrapped for tier %q, want %q", front.tier, environment.TierProduction)
		}
	})

	t.Run("the default edge sees the core it reads before it runs", func(t *testing.T) {
		stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		var sawCore bool
		front := &fakeEdge{kind: "cloudfront", onBootstrap: func() {
			deployed, err := CheckDeployed(context.Background(), stacks, defaultNamespace)
			if err != nil {
				t.Errorf("CheckDeployed inside the edge bootstrap: %v", err)
			}
			sawCore = deployed.EdgeBucket != ""
		}}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		if err := Run(context.Background(), apisFronting(stacks, ssmc, iamc, preloadedStore(), front), defaultNamespace, environment.TierProduction, Request{}, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if !sawCore {
			t.Error("the default edge was bootstrapped before the core it reads the edge bucket from")
		}
	})

	t.Run("preview bootstraps the default edge for the preview tier", func(t *testing.T) {
		front := &fakeEdge{kind: "cloudfront"}
		stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		if err := Run(context.Background(), apisFronting(stacks, ssmc, iamc, preloadedStore(), front), defaultNamespace, environment.TierPreview, Request{}, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if front.tier != environment.TierPreview {
			t.Errorf("the default edge was bootstrapped for tier %q, want %q", front.tier, environment.TierPreview)
		}
	})

	t.Run("a default edge that cannot be bootstrapped stops the run", func(t *testing.T) {
		stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		front := &fakeEdge{kind: "cloudfront", err: errors.New("edge API unreachable")}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		err := Run(context.Background(), apisFronting(stacks, ssmc, iamc, preloadedStore(), front), defaultNamespace, environment.TierProduction, everything(), nil)
		if err == nil {
			t.Fatal("expected Run to fail when the default edge cannot be bootstrapped")
		}
		if slices.Contains(stacks.stacks(), keyStack(environment.TierProduction)) {
			t.Error("provisioned part stacks on top of an edge that never came up")
		}
	})
}

func TestRunDropsCloudflareEdge(t *testing.T) {
	t.Run("the edge's key and stored handles go before its stack", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			tier environment.Tier
		}{
			{"production", environment.TierProduction},
			{"preview", environment.TierPreview},
		} {
			t.Run(tc.name, func(t *testing.T) {
				stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
				frontedBy(t, &fakeEdge{kind: "cloudflare", out: edge.BootstrapOutput{
					ReleasesStore: new(offeredReleasesStore()),
				}})
				apis := apisOf(stacks, ssmc, iamc, preloadedStore())

				if err := Run(context.Background(), apis, defaultNamespace, tc.tier, everything(), nil); err != nil {
					t.Fatalf("run: %v", err)
				}
				names, err := edgeNamesFor(defaultNamespace, tc.tier, KindCloudflare)
				if err != nil {
					t.Fatalf("edgeNamesFor: %v", err)
				}
				for _, param := range names.edgeParams() {
					if _, ok := ssmc.params[param]; !ok {
						t.Fatalf("%s was never stored, so dropping it proves nothing", param)
					}
				}

				req := Request{
					Parts:  []string{provider.VariablesKeyPart},
					Remove: []string{CloudflareEdgePart},
				}
				if err := Run(context.Background(), apis, defaultNamespace, tc.tier, req, nil); err != nil {
					t.Fatalf("dropping the cloudflare edge: %v", err)
				}
				if len(iamc.keys) != 0 {
					t.Errorf("edge reader %s still has %v after its stack was dropped", names.user, iamc.keys)
				}
				for _, param := range names.edgeParams() {
					if _, ok := ssmc.params[param]; ok {
						t.Errorf("%s survived the drop, keeping a live handle to an edge that is gone", param)
					}
				}
				if !slices.Contains(stacks.deleted, edgeStack(tc.tier)) {
					t.Errorf("deleted %v, want the cloudflare edge stack among them", stacks.deleted)
				}
				if _, ok := ssmc.params[originSecretFor(t, tc.tier)]; !ok {
					t.Error("dropping the edge took the origin secret with it; core owns that")
				}
			})
		}
	})
}

func originSecretFor(t *testing.T, tier environment.Tier) string {
	t.Helper()
	names, err := edgeNamesFor(defaultNamespace, tier, KindCloudflare)
	if err != nil {
		t.Fatalf("edgeNamesFor: %v", err)
	}
	return names.originSecretParam
}

func TestUpsertRecoversFailedStacks(t *testing.T) {
	t.Run("a rolled-back part stack reads as absent, not enabled", func(t *testing.T) {
		stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		stacks.blockName(keyStack(environment.TierProduction), 1)
		recordWaits(t)
		if err := Run(context.Background(), apisOf(stacks, ssmc, iamc, preloadedStore()), defaultNamespace, environment.TierProduction,
			Request{Parts: []string{provider.VariablesKeyPart}}, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}

		stacks.wedge(keyStack(environment.TierProduction))
		deployed, err := CheckDeployed(context.Background(), stacks, defaultNamespace)
		if err != nil {
			t.Fatalf("CheckDeployed: %v", err)
		}
		if deployed.Parts.Has(provider.VariablesKeyPart) {
			t.Error("a stack that rolled back and contains nothing reads as an enabled part; every deploy that needs it is waved through")
		}
	})

	t.Run("a rolled-back stack is replaced, not updated into a wall", func(t *testing.T) {
		stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})
		apis := apisOf(stacks, ssmc, iamc, preloadedStore())
		recordWaits(t)

		req := Request{Parts: []string{provider.VariablesKeyPart}}
		if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, req, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		stacks.wedge(keyStack(environment.TierProduction))

		if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, req, nil); err != nil {
			t.Fatalf("re-run over a wedged stack: %v", err)
		}
		if !slices.Contains(stacks.deleted, keyStack(environment.TierProduction)) {
			t.Errorf("deleted %v, want the wedged stack replaced rather than updated", stacks.deleted)
		}
		deployed, err := CheckDeployed(context.Background(), stacks, defaultNamespace)
		if err != nil {
			t.Fatalf("CheckDeployed: %v", err)
		}
		if !deployed.Parts.Has(provider.VariablesKeyPart) {
			t.Error("the re-run left the part missing")
		}
	})

	t.Run("a wedged stack left out of the set is still taken away", func(t *testing.T) {
		stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})
		apis := apisOf(stacks, ssmc, iamc, preloadedStore())
		recordWaits(t)

		if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, Request{Parts: []string{provider.VariablesKeyPart}}, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		stacks.wedge(keyStack(environment.TierProduction))

		if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, Request{Remove: []string{provider.VariablesKeyPart}}, nil); err != nil {
			t.Fatalf("dropping a wedged stack: %v", err)
		}
		if slices.Contains(stacks.stacks(), keyStack(environment.TierProduction)) {
			t.Errorf("stacks left = %v, want the wedged stack gone rather than orphaned", stacks.stacks())
		}
	})

	t.Run("a name still blocked is waited out, not surfaced", func(t *testing.T) {
		stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})
		waits := recordWaits(t)
		stacks.blockName(keyStack(environment.TierProduction), 2)

		if err := Run(context.Background(), apisOf(stacks, ssmc, iamc, preloadedStore()), defaultNamespace, environment.TierProduction,
			Request{Parts: []string{provider.VariablesKeyPart}}, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(*waits) != 2 {
			t.Errorf("waited %d times, want one wait per re-create of the blocked name", len(*waits))
		}
		for i := 1; i < len(*waits); i++ {
			if (*waits)[i] <= (*waits)[i-1] {
				t.Errorf("waits %v do not grow; a fixed name blocked for a minute needs backoff", *waits)
			}
		}
	})

	t.Run("a stack that fails for any other reason is not retried", func(t *testing.T) {
		stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})
		recordWaits(t)
		stacks.blockName(keyStack(environment.TierProduction), 1)
		stacks.reason(keyStack(environment.TierProduction), "Resource handler returned message: Access denied")

		err := Run(context.Background(), apisOf(stacks, ssmc, iamc, preloadedStore()), defaultNamespace, environment.TierProduction,
			Request{Parts: []string{provider.VariablesKeyPart}}, nil)
		if err == nil {
			t.Fatal("retried a failure that will never succeed on retry")
		}
	})
}

func recordWaits(t *testing.T) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	var mu sync.Mutex
	previous := cfn.WaitBefore
	cfn.WaitBefore = func(context.Context, time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		waits = append(waits, cfn.NameBlockedDelay(len(waits)))
		return nil
	}
	t.Cleanup(func() { cfn.WaitBefore = previous })
	return &waits
}

type warningLog struct {
	progress.Log
	warnings []string
}

func (l *warningLog) Warn(message string) { l.warnings = append(l.warnings, message) }

func TestABootstrapRunRefusesAPartItHoldsNoTemplateForBeforeItWritesAnything(t *testing.T) {
	stacks, apis := installedBootstrap(t)
	before := stacks.updates

	err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, Request{Parts: []string{"image-optimizer"}}, nil)

	if err == nil || !strings.Contains(err.Error(), `"image-optimizer"`) {
		t.Fatalf("Run() asked for a part the account bootstrap does not hold = %v, want a refusal naming it", err)
	}
	if stacks.updates != before {
		t.Errorf("Run() wrote %d change sets before refusing a part it cannot apply", stacks.updates-before)
	}
}
