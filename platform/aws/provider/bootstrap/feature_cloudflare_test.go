package bootstrap

import (
	"context"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

type edgeUserTemplate struct {
	Resources map[string]struct {
		Type       string `yaml:"Type"`
		Properties struct {
			UserName string `yaml:"UserName"`
			Policies []struct {
				PolicyName     string `yaml:"PolicyName"`
				PolicyDocument struct {
					Statement []struct {
						Effect    string         `yaml:"Effect"`
						Action    any            `yaml:"Action"`
						Resource  any            `yaml:"Resource"`
						Condition map[string]any `yaml:"Condition"`
					} `yaml:"Statement"`
				} `yaml:"PolicyDocument"`
			} `yaml:"Policies"`
		} `yaml:"Properties"`
	} `yaml:"Resources"`
}

func TestEdgeUser(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
		userName string
	}{
		{string(environment.TierProduction), featureTemplate(FeatureCloudflareEdge, environment.TierProduction), edgeUserName},
		{string(environment.TierPreview), featureTemplate(FeatureCloudflareEdge, environment.TierPreview), previewEdgeUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tmpl edgeUserTemplate
			if err := yaml.Unmarshal([]byte(tc.template), &tmpl); err != nil {
				t.Fatalf("template is not valid YAML: %v", err)
			}
			user, ok := tmpl.Resources["EdgeUser"]
			if !ok {
				t.Fatal("template is missing the EdgeUser resource")
			}
			if user.Type != "AWS::IAM::User" {
				t.Errorf("EdgeUser Type = %q, want AWS::IAM::User", user.Type)
			}
			if user.Properties.UserName != tc.userName {
				t.Errorf("UserName = %q, want %q", user.Properties.UserName, tc.userName)
			}
			if len(user.Properties.Policies) != 1 {
				t.Fatalf("want exactly one inline policy, got %d", len(user.Properties.Policies))
			}

			if name := user.Properties.Policies[0].PolicyName; name != "ocel-edge-cache" {
				t.Errorf("PolicyName = %q, want ocel-edge-cache", name)
			}

			stmts := user.Properties.Policies[0].PolicyDocument.Statement
			var sqsSend, invoke, invokeTagged, invokeTiered bool
			for _, st := range stmts {
				for _, action := range listPolicyActions(st.Action) {
					if strings.HasPrefix(action, "dynamodb:") {
						t.Errorf("the edge user is granted %s, but the edge writes no tag item", action)
					}
				}
				if st.Resource == paramRevalidateQueueARN {
					sqsSend = hasAction(st.Action, "sqs:SendMessage")
				}
				if hasAction(st.Action, "lambda:InvokeFunctionUrl") {
					invoke = true
					if equals, ok := st.Condition["StringEquals"].(map[string]any); ok {
						if equals["aws:ResourceTag/ocel:component"] == "function" {
							invokeTagged = true
						}
						if equals["aws:ResourceTag/ocel:env-tier"] == tc.name {
							invokeTiered = true
						}
					}
				}
			}
			if !sqsSend {
				t.Error("missing sqs:SendMessage on the revalidation queue the isr feature provisioned")
			}
			if !invoke {
				t.Error("missing the lambda:Invoke* grant")
			}
			if !invokeTagged {
				t.Error("lambda:Invoke* grant must be gated on ocel:component being function, so it reaches no listener or other Ocel-run function")
			}
			if !invokeTiered {
				t.Errorf("lambda:Invoke* grant must be gated on ocel:env-tier being %s, or the %s edge's key invokes the other tier's functions too", tc.name, tc.name)
			}
		})
	}
}

type mintingEdge struct {
	*fakeEdge
	hasCred bool
	torn    int
}

func (e *mintingEdge) Bootstrap(_ context.Context, tier environment.Tier) (edge.BootstrapOutput, error) {
	e.bootstraps++
	e.tier = tier
	cred := ""
	if !e.hasCred {
		cred, e.hasCred = "bootstrap-secret", true
	}
	return edge.BootstrapOutput{Offers: []edge.Offer{{
		Kind: edge.OfferReleasesStore,
		Values: map[string]string{
			edge.OfferKeyStoreEndpoint:            "https://releases.example",
			edge.OfferKeyStoreScriptName:          "ocel-releases-store",
			edge.OfferKeyStoreBootstrapCredential: cred,
		},
	}}}, nil
}

func (e *mintingEdge) Teardown(context.Context, environment.Tier) error {
	e.hasCred = false
	e.torn++
	return nil
}

func TestDroppingTheEdgeFeatureLeavesTheNextBootstrapAbleToRun(t *testing.T) {
	ctx := context.Background()
	stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	front := &mintingEdge{fakeEdge: &fakeEdge{kind: "cloudflare"}}
	apis := apisFronting(stacks, ssmc, iamc, preloadedStore(), front)
	fronted := Request{Features: []string{FeatureISR, FeatureCloudflareEdge}}

	if err := Run(ctx, apis, defaultNamespace, environment.TierProduction, fronted, nil); err != nil {
		t.Fatalf("the bootstrap that installs the edge: %v", err)
	}

	drop := Request{Features: []string{FeatureISR}, Remove: []string{FeatureCloudflareEdge}}
	if err := Run(ctx, apis, defaultNamespace, environment.TierProduction, drop, nil); err != nil {
		t.Fatalf("dropping %s: %v", FeatureCloudflareEdge, err)
	}
	if front.torn != 1 {
		t.Errorf("the edge was torn down %d times, want once: the account fronts nothing with it after the drop", front.torn)
	}
	if front.bootstraps != 1 {
		t.Errorf("the edge was bootstrapped %d times, want once: a drop re-adopting what it is about to sever leaves the two disagreeing", front.bootstraps)
	}
	if _, present := ssmc.params[cloudflareNames(environment.TierProduction).releasesStoreParam]; present {
		t.Error("the releases store parameter outlived the drop, so the next bootstrap reads a store for an edge that is no longer installed")
	}

	if err := Run(ctx, apis, defaultNamespace, environment.TierProduction, fronted, nil); err != nil {
		t.Fatalf("a plain bootstrap straight after the drop: %v", err)
	}
}
