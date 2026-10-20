package account

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
)

type baseRoleTemplate struct {
	Resources map[string]struct {
		Type       string `json:"Type"`
		Properties struct {
			RoleName          string   `json:"RoleName"`
			Path              string   `json:"Path"`
			ManagedPolicyArns []string `json:"ManagedPolicyArns"`
			Policies          []struct {
				PolicyDocument struct {
					Statement []policyStatement `json:"Statement"`
				} `json:"PolicyDocument"`
			} `json:"Policies"`
		} `json:"Properties"`
	} `json:"Resources"`
	Outputs map[string]struct {
		Value templateText `json:"Value"`
	} `json:"Outputs"`
}

func coreRoles(t *testing.T, tier environment.Tier) baseRoleTemplate {
	t.Helper()
	return coreRolesOf(t, defaultNamespace, tier)
}

func coreRolesOf(t *testing.T, ns Namespace, tier environment.Tier) baseRoleTemplate {
	t.Helper()
	var tmpl baseRoleTemplate
	if err := json.Unmarshal([]byte(coreBody(ns, tier, "")), &tmpl); err != nil {
		t.Fatalf("the core template is not valid JSON: %v", err)
	}
	return tmpl
}

func roleStatements(t *testing.T, tmpl baseRoleTemplate, logicalID string) []policyStatement {
	t.Helper()
	role, ok := tmpl.Resources[logicalID]
	if !ok {
		t.Fatalf("the core declares no %s", logicalID)
	}
	if role.Type != "AWS::IAM::Role" {
		t.Errorf("%s Type = %q, want AWS::IAM::Role", logicalID, role.Type)
	}
	if len(role.Properties.Policies) != 1 {
		t.Fatalf("%s has %d inline policies, want exactly one", logicalID, len(role.Properties.Policies))
	}
	return role.Properties.Policies[0].PolicyDocument.Statement
}

func resourcesGranted(statements []policyStatement, action string) []string {
	var out []string
	for _, st := range statements {
		if slices.Contains(st.actions(), action) {
			out = append(out, st.resources()...)
		}
	}
	return out
}

func TestTheCoreDeclaresTheTagInvalidatorRoleAtTheBootstrapRolePathUnderItsFixedName(t *testing.T) {
	for _, tc := range []struct {
		tier environment.Tier
		path string
		name string
	}{
		{environment.TierProduction, "/ocel/prod/bootstrap/", "ocel-prod-invalidator"},
		{environment.TierPreview, "/ocel/preview/bootstrap/", "ocel-preview-invalidator"},
	} {
		t.Run(string(tc.tier), func(t *testing.T) {
			tmpl := coreRoles(t, tc.tier)
			roleStatements(t, tmpl, "TagInvalidatorRole")
			role := tmpl.Resources["TagInvalidatorRole"]
			if role.Properties.Path != tc.path {
				t.Errorf("TagInvalidatorRole Path = %q, want %q, the path only its own tier's credentials may pass", role.Properties.Path, tc.path)
			}
			if role.Properties.RoleName != tc.name {
				t.Errorf("TagInvalidatorRole RoleName = %q, want %q, the name the deploy that makes the function runs it as", role.Properties.RoleName, tc.name)
			}
			if want := defaultNamespace.TagInvalidatorRoleARN("111122223333", tc.tier); want != "arn:aws:iam::111122223333:role"+tc.path+tc.name {
				t.Errorf("TagInvalidatorRoleARN() = %q, want the role the core declares", want)
			}
			if got := tmpl.Outputs[outputTagInvalidatorRoleARN].Value; got != "TagInvalidatorRole.Arn" {
				t.Errorf("%s = %q, want the ARN of TagInvalidatorRole", outputTagInvalidatorRoleARN, got)
			}
		})
	}
}

func TestTheTagInvalidatorRoleReadsTheStateTableAndItsStreamOffTheCore(t *testing.T) {
	statements := roleStatements(t, coreRoles(t, environment.TierProduction), "TagInvalidatorRole")
	for _, tc := range []struct{ action, resource string }{
		{"dynamodb:GetRecords", "StateTable.StreamArn"},
		{"dynamodb:GetShardIterator", "StateTable.StreamArn"},
		{"dynamodb:DescribeStream", "StateTable.StreamArn"},
		{"dynamodb:GetItem", "StateTable.Arn"},
	} {
		if got := resourcesGranted(statements, tc.action); !slices.Equal(got, []string{tc.resource}) {
			t.Errorf("%s is granted on %v, want %s alone", tc.action, got, tc.resource)
		}
	}
	for _, st := range statements {
		for _, action := range st.actions() {
			if slices.Contains([]string{"dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:DeleteItem", "dynamodb:Query", "s3:GetObject", "cloudfront:UpdateDistribution"}, action) {
				t.Errorf("TagInvalidatorRole grants %s; the invalidator reads the stream and the state table, invalidates and reports, nothing else", action)
			}
		}
	}
}

func onlyWhatOcelTaggedFor(condition map[string]any, tier environment.Tier) bool {
	return onlyWhatTheNamespaceTaggedFor(condition, defaultNamespace, tier)
}

func onlyWhatTheNamespaceTaggedFor(condition map[string]any, ns Namespace, tier environment.Tier) bool {
	equals, _ := condition["StringEquals"].(map[string]any)
	return equals["aws:ResourceTag/ocel:managed-by"] == string(ns) && equals["aws:ResourceTag/ocel:env-tier"] == string(tier)
}

func TestTheTagInvalidatorRoleInvalidatesTheDistributionsItsOwnNamespaceTagged(t *testing.T) {
	ns := Namespace("acme")
	for _, tier := range bothTiers {
		t.Run(string(tier), func(t *testing.T) {
			for _, st := range roleStatements(t, coreRolesOf(t, ns, tier), "TagInvalidatorRole") {
				if slices.Contains(st.actions(), "cloudfront:CreateInvalidation") && !onlyWhatTheNamespaceTaggedFor(st.Condition, ns, tier) {
					t.Errorf("invalidation condition = %v, want ocel:managed-by %s: a distribution is tagged with the namespace that raised it, so any other value invalidates nothing", st.Condition, ns)
				}
			}
		})
	}
}

func TestTheTagInvalidatorRoleInvalidatesOnlyTheDistributionsOcelTaggedForItsTier(t *testing.T) {
	for _, tier := range bothTiers {
		t.Run(string(tier), func(t *testing.T) {
			statements := roleStatements(t, coreRoles(t, tier), "TagInvalidatorRole")
			want := []string{"arn:aws:cloudfront::${AWS::AccountId}:distribution/*"}
			if got := resourcesGranted(statements, "cloudfront:CreateInvalidation"); !slices.Equal(got, want) {
				t.Errorf("cloudfront:CreateInvalidation is granted on %v, want %v", got, want)
			}
			for _, st := range statements {
				if !slices.Contains(st.actions(), "cloudfront:CreateInvalidation") {
					continue
				}
				if !onlyWhatOcelTaggedFor(st.Condition, tier) {
					t.Errorf("invalidation condition = %v, want it gated on ocel:managed-by being its namespace and ocel:env-tier being %s, or the invalidator flushes distributions Ocel never made", st.Condition, tier)
				}
			}
		})
	}
}

func TestPassingTheTagInvalidatorRoleReachesNothingTheDeployCredentialCannotAlready(t *testing.T) {
	const region, account = "eu-west-1", "111122223333"
	for _, tier := range bothTiers {
		t.Run(string(tier), func(t *testing.T) {
			core, _ := defaultNamespace.StackNameFor(tier)
			table := "arn:aws:dynamodb:" + region + ":" + account + ":table/" + core + "-StateTable-ABC123"
			seed := "arn:aws:ssm:" + region + ":" + account + ":parameter" + cloudflareNames(tier).purgeSeedParam
			resolve := strings.NewReplacer(
				"${AWS::Region}", region,
				"${AWS::AccountId}", account,
				"StateTable.StreamArn", table+"/stream/2026-10-16T00:00:00.000",
				"StateTable.Arn", table,
			)
			_, deployDoc := renderedCredentialsOf(t, tier)
			deploy := grantsOf(t, deployDoc)
			deployReaches := func(action, resource string) bool {
				for g := range deploy {
					if g.action == action && g.condition == "null" && iamResourceMatches(g.resource, resource) {
						return true
					}
				}
				return false
			}
			readsTheSameItems := map[string]struct{ action, resource string }{
				"dynamodb:DescribeStream":   {"dynamodb:Query", table},
				"dynamodb:GetRecords":       {"dynamodb:Query", table},
				"dynamodb:GetShardIterator": {"dynamodb:Query", table},
				"kms:Decrypt":               {"ssm:GetParameter", seed},
			}

			tmpl := coreRoles(t, tier)
			for _, managed := range tmpl.Resources["TagInvalidatorRole"].Properties.ManagedPolicyArns {
				attachable := false
				for g := range deploy {
					attachable = attachable || g.action == "iam:AttachRolePolicy" && strings.Contains(g.condition, `"`+managed+`"`)
				}
				if !attachable {
					t.Errorf("TagInvalidatorRole carries %s, a managed policy the deploy credential may attach to no app role it mints", managed)
				}
			}
			for _, st := range roleStatements(t, tmpl, "TagInvalidatorRole") {
				for _, action := range st.actions() {
					for _, raw := range st.resources() {
						resource := resolve.Replace(raw)
						switch {
						case deployReaches(action, resource), slices.Contains(appBoundaryActions(), action):
						case action == "cloudfront:CreateInvalidation" && onlyWhatOcelTaggedFor(st.Condition, tier):
						case action == "dynamodb:ListStreams" && resource == "*":
						default:
							standIn, ok := readsTheSameItems[action]
							if !ok || !deployReaches(standIn.action, standIn.resource) {
								t.Errorf("TagInvalidatorRole grants %s on %s, which the deploy credential neither grants nor reaches another way, so passing the role to a function it writes escalates it", action, resource)
							}
						}
						if action != "kms:Decrypt" {
							continue
						}
						equals, _ := st.Condition["StringEquals"].(map[string]any)
						via, _ := templateString(equals["kms:ViaService"])
						parameter, _ := templateString(equals["kms:EncryptionContext:PARAMETER_ARN"])
						if resolve.Replace(via) != "ssm."+region+".amazonaws.com" || resolve.Replace(parameter) != seed {
							t.Errorf("kms:Decrypt condition = %v, want it gated on Parameter Store decrypting %s alone, which the deploy credential reads too", st.Condition, seed)
						}
					}
				}
			}
		})
	}
}

func TestTheTagInvalidatorRoleSendsOnlyToItsOwnTiersDeadLetterQueue(t *testing.T) {
	for _, tc := range []struct {
		tier         environment.Tier
		queue, other string
	}{
		{environment.TierProduction, "ocel-prod-invalidator", "ocel-preview-invalidator"},
		{environment.TierPreview, "ocel-preview-invalidator", "ocel-prod-invalidator"},
	} {
		t.Run(string(tc.tier), func(t *testing.T) {
			statements := roleStatements(t, coreRoles(t, tc.tier), "TagInvalidatorRole")
			want := []string{"arn:aws:sqs:${AWS::Region}:${AWS::AccountId}:" + tc.queue}
			got := resourcesGranted(statements, "sqs:SendMessage")
			if !slices.Equal(got, want) {
				t.Errorf("sqs:SendMessage is granted on %v, want %v", got, want)
			}
			if slices.ContainsFunc(got, func(resource string) bool { return strings.HasSuffix(resource, ":"+tc.other) }) {
				t.Errorf("the %s invalidator may send to %s, the other tier's queue", tc.tier, tc.other)
			}
		})
	}
}

func TestTheTagInvalidatorRoleReadsOnlyItsOwnTiersPurgeSeed(t *testing.T) {
	for _, tier := range bothTiers {
		t.Run(string(tier), func(t *testing.T) {
			own := cloudflareNames(tier).purgeSeedParam
			other := cloudflareNames(tier.Sibling()).purgeSeedParam
			statements := roleStatements(t, coreRoles(t, tier), "TagInvalidatorRole")
			want := []string{"arn:aws:ssm:${AWS::Region}:${AWS::AccountId}:parameter" + own}
			if got := resourcesGranted(statements, "ssm:GetParameter"); !slices.Equal(got, want) {
				t.Errorf("ssm:GetParameter is granted on %v, want %v", got, want)
			}
			body := coreBody(defaultNamespace, tier, "")
			if strings.Contains(body, "parameter"+other+`"`) {
				t.Errorf("the %s core reaches %s, the other tier's purge seed", tier, other)
			}
			if len(resourcesGranted(statements, "kms:Decrypt")) == 0 {
				t.Error("TagInvalidatorRole cannot decrypt the seed it reads")
			}
		})
	}
}

func TestTheEdgeInvokeRoleInvokesOnlyItsOwnTiersOcelFunctions(t *testing.T) {
	for _, tier := range bothTiers {
		t.Run(string(tier), func(t *testing.T) {
			tmpl := coreRoles(t, tier)
			statements := roleStatements(t, tmpl, "EdgeInvokeRole")
			if got, want := tmpl.Resources["EdgeInvokeRole"].Properties.RoleName, defaultNamespace.EdgeInvokeRoleName(tier); got != want {
				t.Errorf("EdgeInvokeRole RoleName = %q, want %q", got, want)
			}
			if got := tmpl.Outputs[OutputEdgeInvokeRoleARN].Value; got != "EdgeInvokeRole.Arn" {
				t.Errorf("%s = %q, want the ARN of EdgeInvokeRole", OutputEdgeInvokeRoleARN, got)
			}
			invokes := 0
			for _, st := range statements {
				if !slices.Contains(st.actions(), "lambda:InvokeFunction") {
					continue
				}
				invokes++
				equals, _ := st.Condition["StringEquals"].(map[string]any)
				if equals["aws:ResourceTag/ocel:component"] != "function" {
					t.Errorf("invoke condition = %v, want it gated on ocel:component being function, so API Gateway reaches no listener or other Ocel-run function", st.Condition)
				}
				if equals["aws:ResourceTag/ocel:env-tier"] != string(tier) {
					t.Errorf("invoke condition = %v, want it gated on ocel:env-tier being %s, or a %s REST API fronts the other tier's functions too", st.Condition, tier, tier)
				}
			}
			if invokes != 1 {
				t.Errorf("EdgeInvokeRole has %d invoke statements, want exactly one", invokes)
			}
		})
	}
}
