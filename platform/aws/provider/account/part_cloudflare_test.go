package account

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

type edgeUserTemplate struct {
	Resources map[string]struct {
		Type       string `json:"Type"`
		Properties struct {
			UserName string `json:"UserName"`
			Policies []struct {
				PolicyName     string `json:"PolicyName"`
				PolicyDocument struct {
					Statement []policyStatement `json:"Statement"`
				} `json:"PolicyDocument"`
			} `json:"Policies"`
		} `json:"Properties"`
	} `json:"Resources"`
}

func TestEdgeUser(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
		userName string
	}{
		{string(environment.TierProduction), partTemplate(CloudflareEdgePart, environment.TierProduction), edgeUserName},
		{string(environment.TierPreview), partTemplate(CloudflareEdgePart, environment.TierPreview), previewEdgeUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tmpl edgeUserTemplate
			if err := json.Unmarshal([]byte(tc.template), &tmpl); err != nil {
				t.Fatalf("template is not valid JSON: %v", err)
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
			var invoke, invokeTagged, invokeTiered, listBucket bool
			for _, st := range stmts {
				for _, action := range listPolicyActions(st.Action) {
					if strings.HasPrefix(action, "dynamodb:") {
						t.Errorf("the edge user is granted %s, but the edge writes no tag item", action)
					}
					if strings.HasPrefix(action, "sqs:") || strings.HasPrefix(action, "kms:") {
						t.Errorf("the edge user is granted %s, but the edge sends nothing to a queue", action)
					}
				}
				if hasAction(st.Action, "s3:ListBucket") {
					listBucket = len(listPolicyActions(st.Action)) == 1 && slices.Equal(templateStrings(st.Resource), []string{paramEdgeBucketARN})
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
			if !readsTheEdgeBucketAlone(stmts) {
				t.Error("the edge user must read every object in the edge bucket, whatever its tags, and no object in any other bucket")
			}
			if !listBucket {
				t.Error("missing s3:ListBucket, alone, on the edge bucket itself, so an absent object answers 404 rather than 403")
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

func readsTheEdgeBucketAlone(stmts []policyStatement) bool {
	const release = "shop/prod/web/r1a2b3c4d/"
	for _, object := range []string{
		"assets/" + release + "_next/static/app.js",
		"pages/" + release + "index.cache.json",
		"route-tables/" + release + strings.Repeat("a", 64) + ".json",
		"bundles/" + release + "bundle.json",
		"image-config/" + release + "image-config.json",
	} {
		if !edgeUserReads(stmts, "arn:aws:s3:::edge/"+object) {
			return false
		}
	}
	for _, other := range []string{"arn:aws:s3:::cache/use-cache/" + release + "entry.json", "arn:aws:s3:::state/trust/client-cas.json", "arn:aws:s3:::artifacts/fn/" + release + "api/h.zip"} {
		if edgeUserReads(stmts, other) {
			return false
		}
	}
	for _, st := range stmts {
		if slices.Contains(st.actions(), "s3:GetObject") && len(st.Condition) > 0 {
			return false
		}
	}
	return true
}

func edgeUserReads(stmts []policyStatement, object string) bool {
	for _, st := range stmts {
		if st.Effect == "Allow" && slices.Contains(st.actions(), "s3:GetObject") && iamResourceMatchesAny(st.resources(), object) {
			return true
		}
	}
	return false
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
	return edge.BootstrapOutput{ReleasesStore: &edge.ReleasesStore{
		Name:                "ocel-releases-store",
		Endpoint:            "https://releases.example",
		BootstrapCredential: cred,
	}}, nil
}

func (e *mintingEdge) Teardown(context.Context, environment.Tier) error {
	e.hasCred = false
	e.torn++
	return nil
}

func TestDroppingTheEdgePartLeavesTheNextBootstrapAbleToRun(t *testing.T) {
	ctx := context.Background()
	stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	front := &mintingEdge{fakeEdge: &fakeEdge{kind: "cloudflare"}}
	apis := apisFronting(stacks, ssmc, iamc, preloadedStore(), front)
	fronted := Request{Parts: []string{CloudflareEdgePart}}

	if err := Run(ctx, apis, defaultNamespace, environment.TierProduction, fronted, nil); err != nil {
		t.Fatalf("the bootstrap that installs the edge: %v", err)
	}

	drop := Request{Remove: []string{CloudflareEdgePart}}
	if err := Run(ctx, apis, defaultNamespace, environment.TierProduction, drop, nil); err != nil {
		t.Fatalf("dropping %s: %v", CloudflareEdgePart, err)
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

func TestTheEdgeUserInvokesTheImageOptimizerOfItsOwnTierByNameAndNoOtherFunction(t *testing.T) {
	for _, tc := range []struct {
		tier      environment.Tier
		optimizer string
	}{
		{environment.TierProduction, "ocel-prod-optimizer"},
		{environment.TierPreview, "ocel-preview-optimizer"},
	} {
		t.Run(string(tc.tier), func(t *testing.T) {
			var tmpl edgeUserTemplate
			if err := json.Unmarshal([]byte(partTemplate(CloudflareEdgePart, tc.tier)), &tmpl); err != nil {
				t.Fatalf("template is not valid JSON: %v", err)
			}
			want := "arn:aws:lambda:${AWS::Region}:${AWS::AccountId}:function:" + tc.optimizer
			var named []string
			for _, st := range tmpl.Resources["EdgeUser"].Properties.Policies[0].PolicyDocument.Statement {
				if !hasAction(st.Action, "lambda:InvokeFunction") {
					continue
				}
				for _, resource := range templateStrings(st.Resource) {
					if strings.HasSuffix(resource, ":function:*") {
						continue
					}
					named = append(named, resource)
					if st.Condition != nil {
						t.Errorf("the invoke grant on %s is conditioned on %v, and the optimizer carries no app function's tags", resource, st.Condition)
					}
				}
			}
			if !slices.Equal(named, []string{want}) {
				t.Errorf("the edge user invokes %v by name, want %s alone", named, want)
			}
		})
	}
}
