package account

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/kv"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/postgres"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/realtime"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/envsourcesync"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/imageoptimizer"
)

func ocelRoleOf(tier environment.Tier) requestContext {
	return taggedWithTier(tier, requestContext{"aws:ResourceTag/" + iam.ManagedByTagKey: string(defaultNamespace)})
}

func partRoleOf(tier environment.Tier) requestContext {
	return taggedWithTier(tier, requestContext{
		"aws:ResourceTag/" + iam.ManagedByTagKey: string(defaultNamespace),
		"aws:PrincipalAccount":                   "111122223333",
		"iam:PermissionsBoundary":                "arn:aws:iam::111122223333:policy/" + defaultNamespace.AppBoundaryNameFor(tier),
	})
}

func partRolesOf(tier environment.Tier) []string {
	path := "arn:aws:iam::111122223333:role" + defaultNamespace.AppRolePathFor(tier)
	sync := envsourcesync.NamesFor(string(defaultNamespace), tier)
	return []string{path + sync.Role, path + sync.ScheduleRole, path + imageoptimizer.NamesFor(string(defaultNamespace), tier).Role}
}

func TestNoCredentialRepointsTheTrustPolicyOfAnAppRole(t *testing.T) {
	for _, tier := range bothTiers {
		bootstrapDoc, deployDoc := renderedCredentialsOf(t, tier)
		if actionsOf(t, deployDoc)["iam:UpdateAssumeRolePolicy"] {
			t.Errorf("the %s deploy credential grants iam:UpdateAssumeRolePolicy, which hands an app role's trust policy to whoever has the credential", tier)
		}
		appRole := "arn:aws:iam::111122223333:role" + defaultNamespace.AppRolePathFor(tier) + "ocel-app-web-production"
		if allows(t, bootstrapDoc, "iam:UpdateAssumeRolePolicy", appRole, ocelRoleOf(tier)) {
			t.Errorf("the %s bootstrap credential can repoint the trust policy of %s, an app role no bootstrap part runs as", tier, appRole)
		}
	}
}

func TestABootstrapCredentialRepointsTheTrustPolicyOfOnlyItsOwnTiersPartRoles(t *testing.T) {
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		bootstrapDoc, _ := renderedCredentialsOf(t, tier)
		for _, role := range partRolesOf(tier) {
			if !allows(t, bootstrapDoc, "iam:UpdateAssumeRolePolicy", role, partRoleOf(tier)) {
				t.Errorf("the %s bootstrap credential cannot put back the trust policy of %s, a role one of its parts runs as", tier, role)
			}
			if allows(t, bootstrapDoc, "iam:UpdateAssumeRolePolicy", role, ocelRoleOf(tier)) {
				t.Errorf("the %s bootstrap credential can repoint the trust policy of %s while the role is not under the app boundary", tier, role)
			}
		}
		for _, role := range partRolesOf(sibling) {
			if allows(t, bootstrapDoc, "iam:UpdateAssumeRolePolicy", role, partRoleOf(sibling)) {
				t.Errorf("the %s bootstrap credential can repoint the trust policy of %s, a role of the %s tier's parts", tier, role, sibling)
			}
		}
	}
}

func TestTheDeployCredentialWritesNoCloudFormationStack(t *testing.T) {
	for g := range grantsOf(t, mustRender(t, DeployCredentialPermissions)) {
		if strings.HasPrefix(g.action, "cloudformation:") && !readOnly(g.action) {
			t.Errorf("the deploy credential grants %s on %s, and a deploy writes no CloudFormation stack", g.action, g.resource)
		}
	}
}

func TestEveryCredentialListsTheResourcesOfEveryBootstrapStackOfItsTier(t *testing.T) {
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		bootstrapDoc, deployDoc := renderedCredentialsOf(t, tier)
		for purpose, document := range map[string]string{"bootstrap": bootstrapDoc, "deploy": deployDoc} {
			grants := grantsOf(t, document)
			for _, stack := range defaultNamespace.Scope(tier).BootstrapStacks {
				if !grants[grant{action: "cloudformation:ListStackResources", resource: stack, condition: conditionJSON(t, nil)}] {
					t.Errorf("the %s %s credential does not grant cloudformation:ListStackResources on %s, so `ocel bootstrap status --resources` cannot say what that stack holds", purpose, tier, stack)
				}
			}
		}
	}
}

func TestEveryCredentialMayListTheRegionsItsRegionIsCheckedAgainst(t *testing.T) {
	for purpose, document := range bothCredentials(t) {
		if !grantsOf(t, document)[grant{action: "ec2:DescribeRegions", resource: iam.AnyResource, condition: conditionJSON(t, nil)}] {
			t.Errorf("the %s credential does not grant ec2:DescribeRegions on %q, so a mistyped region meets the cloud's own error rather than the region meant", purpose, iam.AnyResource)
		}
	}
}

func TestOnlyTheEdgeUserIsMintedAndItHasNoManagedPolicy(t *testing.T) {
	for _, tier := range bothTiers {
		for purpose, document := range credentialsOfTier(t, tier) {
			for g := range grantsOf(t, document) {
				if g.action == "iam:AttachUserPolicy" {
					t.Errorf("the %s %s credential grants iam:AttachUserPolicy, which turns a minted user into whatever policy it names", tier, purpose)
				}
				if !strings.HasPrefix(g.action, "iam:") || !strings.Contains(g.action, "User") && !strings.Contains(g.action, "AccessKey") {
					continue
				}
				if g.resource != defaultNamespace.Scope(tier).EdgeUser {
					t.Errorf("the %s %s credential grants %s on %q, which is not its tier's edge user", tier, purpose, g.action, g.resource)
				}
			}
		}
	}

	bootstrapActions := actionsOf(t, mustRender(t, BootstrapCredentialPermissions))
	for _, action := range []string{
		"iam:CreateUser",
		"iam:PutUserPolicy",
		"iam:CreateAccessKey",
		"iam:ListAccessKeys",
		"iam:DeleteAccessKey",
	} {
		if !bootstrapActions[action] {
			t.Errorf("the bootstrap credential does not grant %s, which minting the edge user needs", action)
		}
	}
}

func TestOnlyTheBootstrapCredentialDeletesThePulumiPassphraseAndOnlyByItsExactPath(t *testing.T) {
	r := defaultNamespace.Scope(environment.TierProduction)
	if _, path, _ := strings.Cut(r.PassphraseParameter, ":parameter"); strings.ContainsAny(path, "*?") {
		t.Fatalf("the passphrase ARN %q names a parameter pattern, and the one delete grant on it must name the path exactly", r.PassphraseParameter)
	}
	for purpose, document := range bothCredentials(t) {
		for g := range grantsOf(t, document) {
			if !strings.HasPrefix(g.action, "ssm:Delete") || !iamResourceMatches(g.resource, r.PassphraseParameter) {
				continue
			}
			if !strings.HasPrefix(purpose, "bootstrap") {
				t.Errorf("the %s credential grants %s on %s, which reaches %s: only the credential that can already destroy every Pulumi state in the account may take what encrypts it", purpose, g.action, g.resource, r.PassphraseParameter)
				continue
			}
			if g.action != "ssm:DeleteParameter" || g.resource != r.PassphraseParameter || g.condition != conditionJSON(t, nil) {
				t.Errorf("the bootstrap credential grants %s on %s under %s, want ssm:DeleteParameter on exactly %s: a wider grant reaches the passphrase by accident from a tree it was meant to prune", g.action, g.resource, g.condition, r.PassphraseParameter)
			}
		}
	}
	bootstrapGrants := grantsOf(t, mustRender(t, BootstrapCredentialPermissions))
	for _, resource := range slices.Concat([]string{r.OriginParameter, r.PassphraseParameter}, r.EdgeParameters) {
		if !bootstrapGrants[grant{action: "ssm:DeleteParameter", resource: resource, condition: conditionJSON(t, nil)}] {
			t.Errorf("the bootstrap credential cannot delete %s, which a teardown reclaims", resource)
		}
	}
}

func TestABootstrapCredentialWritesAndDeletesThePassphraseOfOnlyItsOwnTier(t *testing.T) {
	const account = "arn:aws:ssm:us-east-1:111122223333:parameter"
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		bootstrapDoc, _ := renderedCredentialsOf(t, tier)
		own := account + defaultNamespace.PassphraseParamFor(tier)
		other := account + defaultNamespace.PassphraseParamFor(sibling)
		for _, action := range []string{"ssm:GetParameter", "ssm:PutParameter", "ssm:DeleteParameter"} {
			if !allows(t, bootstrapDoc, action, own, nil) {
				t.Errorf("the %s bootstrap credential cannot %s on %s, its own tier's passphrase", tier, action, own)
			}
			if allows(t, bootstrapDoc, action, other, nil) {
				t.Errorf("the %s bootstrap credential can %s on %s, the passphrase the %s tier's Pulumi state is encrypted under", tier, action, other, sibling)
			}
		}
	}
}

func TestEveryCredentialMayGrantLambdaTheVariablesKeyOfItsTierAndNoOther(t *testing.T) {
	for _, tier := range bothTiers {
		want := conditionJSON(t, iam.MergeConditions(defaultNamespace.Scope(tier).AliasedAsItsVariablesKey(), map[string]any{
			"StringEquals": map[string]any{"aws:ResourceTag/" + VariablesKeyComponentTagKey: VariablesKeyComponentTagValue},
			"Bool":         map[string]any{"kms:GrantIsForAWSResource": "true"},
		}))
		for purpose, document := range credentialsOfTier(t, tier) {
			grants := grantsOf(t, document)
			if !grants[grant{action: "kms:CreateGrant", resource: iam.AnyKeyARN, condition: want}] {
				t.Errorf("the %s %s credential does not grant kms:CreateGrant on its tier's variables key for an AWS service, so Lambda cannot seal a function's environment under it", tier, purpose)
			}
			for g := range grants {
				if g.action == "kms:CreateGrant" && g.condition != want {
					t.Errorf("the %s %s credential grants kms:CreateGrant under %s, which lets the credential hand any principal a key it never made", tier, purpose, g.condition)
				}
			}
		}
	}
}

func TestEveryCredentialPublishesOnlyTheRuntimeLayers(t *testing.T) {
	for _, tier := range bothTiers {
		r := defaultNamespace.Scope(tier)
		for purpose, document := range credentialsOfTier(t, tier) {
			testRuntimeLayerGrants(t, string(tier)+" "+purpose, document, r)
		}
	}
}

func TestOnlyTheBootstrapCredentialReclaimsTheRuntimeLayers(t *testing.T) {
	for _, tier := range bothTiers {
		r := defaultNamespace.Scope(tier)
		bootstrapDoc, deployDoc := renderedCredentialsOf(t, tier)
		bootstrapGrants := grantsOf(t, bootstrapDoc)
		for _, version := range r.RuntimeLayerVersions {
			if !bootstrapGrants[grant{action: "lambda:DeleteLayerVersion", resource: version, condition: conditionJSON(t, nil)}] {
				t.Errorf("the %s bootstrap credential does not grant lambda:DeleteLayerVersion on %s, so a destroy leaves the tier's runtime layers behind", tier, version)
			}
		}
		for _, document := range []string{bootstrapDoc, deployDoc} {
			for g := range grantsOf(t, document) {
				if g.action == "lambda:ListLayers" {
					t.Errorf("a %s credential grants lambda:ListLayers on %s, and a destroy reaches the tier's runtime layers by the names it recorded", tier, g.resource)
				}
			}
		}
		for g := range grantsOf(t, deployDoc) {
			if g.action == "lambda:DeleteLayerVersion" {
				t.Errorf("the %s deploy credential grants lambda:DeleteLayerVersion on %s, and a deploy never deletes a runtime another release boots through", tier, g.resource)
			}
		}
	}
}

func testRuntimeLayerGrants(t *testing.T, purpose, document string, r iam.Scope) {
	t.Helper()
	grants := grantsOf(t, document)
	for _, action := range []string{"lambda:PublishLayerVersion", "lambda:GetLayerVersion"} {
		for _, version := range r.RuntimeLayerVersions {
			if !grants[grant{action: action, resource: version, condition: conditionJSON(t, nil)}] {
				t.Errorf("the %s credential does not grant %s on %s, so a deploy cannot publish or boot through this build's runtime", purpose, action, version)
			}
		}
	}
	if !grants[grant{action: "lambda:ListLayerVersions", resource: iam.AnyResource, condition: conditionJSON(t, nil)}] {
		t.Errorf("the %s credential does not grant lambda:ListLayerVersions on %q, the only resource IAM evaluates it against, so a deploy cannot find the runtime already published", purpose, iam.AnyResource)
	}
	for g := range grants {
		if !strings.Contains(g.action, "LayerVersion") || g.action == "lambda:ListLayerVersions" {
			continue
		}
		if !slices.Contains(r.RuntimeLayers, g.resource) && !slices.Contains(r.RuntimeLayerVersions, g.resource) {
			t.Errorf("the %s credential grants %s on %s, which reaches layers Ocel never published", purpose, g.action, g.resource)
		}
	}
}

func TestBothCredentialsAdmitABroughtVariablesKeyByItsARNToWhatOcelCallsOnItAndNothingMore(t *testing.T) {
	const brought = "arn:aws:kms:us-east-1:123456789012:key/1234abcd-12ab-34cd-56ef-1234567890ab"
	want := map[grant]bool{
		{action: "kms:Decrypt", resource: brought, condition: conditionJSON(t, nil)}:     true,
		{action: "kms:DescribeKey", resource: brought, condition: conditionJSON(t, nil)}: true,
		{action: "kms:Encrypt", resource: brought, condition: conditionJSON(t, nil)}:     true,
		{action: "kms:CreateGrant", resource: brought, condition: conditionJSON(t, map[string]any{
			"Bool": map[string]any{"kms:GrantIsForAWSResource": "true"},
		})}: true,
	}
	for purpose, render := range map[string]func(Namespace, environment.Tier, string) (string, error){
		"bootstrap": BootstrapCredentialPermissions,
		"deploy":    DeployCredentialPermissions,
	} {
		without := grantsOf(t, mustRender(t, render))
		document, err := render(defaultNamespace, environment.TierProduction, brought)
		if err != nil {
			t.Fatalf("render the %s credential with a brought key: %v", purpose, err)
		}
		with := grantsOf(t, document)

		for g := range without {
			if !with[g] {
				t.Errorf("the %s credential drops %s on %s when a key is brought", purpose, g.action, g.resource)
			}
		}
		added := map[grant]bool{}
		for g := range with {
			if !without[g] {
				added[g] = true
			}
		}
		if !maps.Equal(added, want) {
			t.Errorf("the %s credential adds %v for a brought key, want %v", purpose, added, want)
		}
	}
}

func TestEveryCredentialOpensOnlyTheVariablesKeyOfItsOwnTier(t *testing.T) {
	opening := []string{"kms:Decrypt", "kms:Encrypt", "kms:GenerateDataKey", "kms:CreateGrant"}
	for _, tier := range bothTiers {
		want := map[string]any{"kms:ResourceAliases": defaultNamespace.variablesKeyAliasFor(tier)}
		for purpose, document := range credentialsOfTier(t, tier) {
			opened := map[string]bool{}
			for _, statement := range parsePolicy(t, document).Statement {
				for _, action := range stringsOf(t, statement.Action, "Action") {
					if !slices.Contains(opening, action) {
						continue
					}
					opened[action] = true
					if got := statement.Condition["ForAnyValue:StringEquals"]; !reflect.DeepEqual(got, want) {
						t.Errorf("the %s %s credential grants %s under ForAnyValue:StringEquals %v, want %v: any other key reaches the %s tier's variables", tier, purpose, action, got, want, tier.Sibling())
					}
				}
			}
			for _, action := range opening {
				if !opened[action] {
					t.Errorf("the %s %s credential grants no %s, and a deploy seals and opens variables with it", tier, purpose, action)
				}
			}
		}
	}
}

func TestEveryCredentialReachesOnlyTheStateAndVariablesOfItsOwnTier(t *testing.T) {
	stack := map[environment.Tier]string{}
	bucket := func(tier environment.Tier, class naming.StorageClass) string {
		name, err := defaultNamespace.BootstrapBucketName(tier, class, "1a2b3c")
		if err != nil {
			t.Fatal(err)
		}
		return "arn:aws:s3:::" + name
	}
	for _, tier := range bothTiers {
		stack[tier], _ = defaultNamespace.StackNameFor(tier)
	}
	const tableARN = "arn:aws:dynamodb:us-east-1:111122223333:table/"
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		for purpose, document := range credentialsOfTier(t, tier) {
			for _, c := range []struct{ action, own, other string }{
				{"s3:GetObject", bucket(tier, naming.StorageState) + "/state", bucket(sibling, naming.StorageState) + "/state"},
				{"s3:ListBucket", bucket(tier, naming.StorageState), bucket(sibling, naming.StorageState)},
				{"s3:PutObject", bucket(tier, naming.StorageArtifacts) + "/code.zip", bucket(sibling, naming.StorageArtifacts) + "/code.zip"},
				{"s3:PutObject", bucket(tier, naming.StorageEdge) + "/assets/app.js", bucket(sibling, naming.StorageEdge) + "/assets/app.js"},
				{"s3:DeleteObject", bucket(tier, naming.StorageCache) + "/use-cache/entry.json", bucket(sibling, naming.StorageCache) + "/use-cache/entry.json"},
				{"dynamodb:GetItem", tableARN + stack[tier] + "-VariablesTable-1A2B3C4D5E6F", tableARN + stack[sibling] + "-VariablesTable-1A2B3C4D5E6F"},
				{"dynamodb:Query", tableARN + stack[tier] + "-VariablesTable-1A2B3C4D5E6F/index/gsi1", tableARN + stack[sibling] + "-VariablesTable-1A2B3C4D5E6F/index/gsi1"},
				{"dynamodb:PutItem", tableARN + stack[tier] + "-StateTable-1A2B3C4D5E6F", tableARN + stack[sibling] + "-StateTable-1A2B3C4D5E6F"},
			} {
				if !reaches(t, document, c.action, c.own) {
					t.Errorf("the %s %s credential cannot %s on %s, its own tier's store", tier, purpose, c.action, c.own)
				}
				if reaches(t, document, c.action, c.other) {
					t.Errorf("the %s %s credential can %s on %s, the %s tier's store", tier, purpose, c.action, c.other, sibling)
				}
			}
		}
	}
}

func TestEveryBucketAndTableABootstrapStackMakesIsOneTheCredentialsName(t *testing.T) {
	for _, tier := range bothTiers {
		buckets := defaultNamespace.Scope(tier).BootstrapBuckets
		templates := map[string]string{"core": coreBody(defaultNamespace, tier, "")}
		for _, f := range partRegistry {
			templates[f.name] = partTemplate(f.name, tier)
		}
		for stack, body := range templates {
			for logicalID, resource := range parseTemplateStr(t, body).Resources {
				switch {
				case resource.Type == "AWS::S3::Bucket" && (stack != "core" || !iamResourceMatchesAny(buckets, "arn:aws:s3:::"+resource.Properties.BucketName)),
					resource.Type == "AWS::DynamoDB::Table" && (stack != "core" || !slices.Contains(coreStackTables, logicalID)):
					t.Errorf("the %s %s stack makes %s %s, whose name no credential document reaches", tier, stack, resource.Type, logicalID)
				}
			}
		}
	}
}

func TestEveryRoleABootstrapStackGeneratesIsOneOnlyItsOwnTiersCredentialMayPass(t *testing.T) {
	passing := []requestContext{
		{"iam:PassedToService": iam.LambdaPrincipal},
		{"iam:PassedToService": iam.SchedulerPrincipal},
	}
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		ownDoc, _ := renderedCredentialsOf(t, tier)
		siblingDoc, _ := renderedCredentialsOf(t, sibling)
		templates := map[string]string{"core": coreBody(defaultNamespace, tier, "")}
		for _, f := range partRegistry {
			templates[f.stackName(defaultNamespace, tier)] = partTemplate(f.name, tier)
		}
		for stack, body := range templates {
			for logicalID, resource := range parseVariablesTemplate(t, body).Resources {
				if resource.Type != "AWS::IAM::Role" || resource.Properties.RoleName != "" {
					continue
				}
				path := resource.Properties.Path
				if path == "" {
					path = "/"
				}
				role := "arn:aws:iam::111122223333:role" + path + stack + "-" + logicalID + "-A1B2C3D4E5F6"
				passable := slices.ContainsFunc(passing, func(request requestContext) bool {
					return allows(t, ownDoc, "iam:PassRole", role, taggedWithTier(tier, request))
				})
				if !passable {
					t.Errorf("the %s bootstrap credential cannot pass %s, the role its %s stack generates as %s", tier, role, stack, logicalID)
				}
				for _, request := range passing {
					if allows(t, siblingDoc, "iam:PassRole", role, taggedWithTier(sibling, request)) {
						t.Errorf("the %s bootstrap credential can pass %s to %s, the role the %s tier's %s stack generates", sibling, role, request["iam:PassedToService"], tier, stack)
					}
				}
			}
		}
	}
}

func TestOnlyTheBootstrapCredentialOfATierManagesItsBoundaryPolicyAndVariablesAlias(t *testing.T) {
	const account = "arn:aws:%s::111122223333:%s"
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		bootstrapDoc, deployDoc := renderedCredentialsOf(t, tier)
		for _, c := range []struct{ action, own, other string }{
			{"iam:CreatePolicyVersion", fmt.Sprintf(account, "iam", "policy/"+defaultNamespace.AppBoundaryNameFor(tier)), fmt.Sprintf(account, "iam", "policy/"+defaultNamespace.AppBoundaryNameFor(sibling))},
			{"kms:UpdateAlias", fmt.Sprintf(account, "kms", defaultNamespace.variablesKeyAliasFor(tier)), fmt.Sprintf(account, "kms", defaultNamespace.variablesKeyAliasFor(sibling))},
		} {
			if !reaches(t, bootstrapDoc, c.action, c.own) {
				t.Errorf("the %s bootstrap credential cannot %s on %s", tier, c.action, c.own)
			}
			if reaches(t, bootstrapDoc, c.action, c.other) {
				t.Errorf("the %s bootstrap credential can %s on %s, the %s tier's", tier, c.action, c.other, sibling)
			}
			if reaches(t, deployDoc, c.action, c.own) {
				t.Errorf("the %s deploy credential can %s on %s", tier, c.action, c.own)
			}
		}
	}
}

const variablesKeyARN = "arn:aws:kms:us-east-1:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab"

func variablesKeyOf(tier environment.Tier) requestContext {
	return requestContext{
		"aws:ResourceTag/" + VariablesKeyComponentTagKey: VariablesKeyComponentTagValue,
		"aws:ResourceTag/" + naming.EnvTierTagKey:        string(tier),
		"kms:ResourceAliases":                            defaultNamespace.variablesKeyAliasFor(tier),
	}
}

func TestABootstrapCredentialRetargetsAndManagesOnlyTheVariablesKeyOfItsOwnTier(t *testing.T) {
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		bootstrapDoc, _ := renderedCredentialsOf(t, tier)
		for _, action := range []string{
			"kms:CreateAlias", "kms:UpdateAlias", "kms:DeleteAlias",
			"kms:PutKeyPolicy", "kms:ScheduleKeyDeletion", "kms:DisableKey", "kms:TagResource", "kms:UntagResource",
		} {
			if !allows(t, bootstrapDoc, action, variablesKeyARN, variablesKeyOf(tier)) {
				t.Errorf("the %s bootstrap credential cannot %s on its own tier's variables key", tier, action)
			}
			if allows(t, bootstrapDoc, action, variablesKeyARN, variablesKeyOf(sibling)) {
				t.Errorf("the %s bootstrap credential can %s on the %s tier's variables key, so it can point its own alias at that key or take the key over", tier, action, sibling)
			}
		}
	}
}

func TestABootstrapCredentialReachesTheBaseRolesOfOnlyItsOwnTier(t *testing.T) {
	roleOf := func(tier environment.Tier) string {
		return defaultNamespace.TagInvalidatorRoleARN("111122223333", tier)
	}
	toLambda := requestContext{"iam:PassedToService": iam.LambdaPrincipal}
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		bootstrapDoc, _ := renderedCredentialsOf(t, tier)
		role, siblingRole := roleOf(tier), roleOf(sibling)
		for _, c := range []struct {
			action, arn, siblingARN string
			also                    requestContext
		}{
			{"iam:PutRolePolicy", role, siblingRole, nil},
			{"iam:DeleteRolePolicy", role, siblingRole, nil},
			{"iam:UpdateAssumeRolePolicy", role, siblingRole, nil},
			{"iam:UpdateRole", role, siblingRole, nil},
			{"iam:DeleteRole", role, siblingRole, nil},
			{"iam:UntagRole", role, siblingRole, nil},
			{"iam:TagRole", role, siblingRole, nil},
			{"iam:AttachRolePolicy", role, siblingRole, requestContext{"iam:PolicyARN": iam.LambdaBasicExecutionPolicyARN}},
			{"iam:PassRole", role, siblingRole, toLambda},
		} {
			if !allows(t, bootstrapDoc, c.action, c.arn, taggedWithTier(tier, c.also)) {
				t.Errorf("the %s bootstrap credential cannot %s on %s, which its own stacks made", tier, c.action, c.arn)
			}
			if allows(t, bootstrapDoc, c.action, c.siblingARN, taggedWithTier(sibling, c.also)) {
				t.Errorf("the %s bootstrap credential can %s on %s, which the %s tier's stacks made", tier, c.action, c.siblingARN, sibling)
			}
		}
		for _, c := range []struct{ action, arn string }{
			{"iam:CreateRole", role},
		} {
			creating := func(of environment.Tier) requestContext {
				return requestContext{"aws:RequestTag/" + naming.EnvTierTagKey: string(of)}
			}
			if !allows(t, bootstrapDoc, c.action, c.arn, creating(tier)) {
				t.Errorf("the %s bootstrap credential cannot %s tagged for its own tier", tier, c.action)
			}
			if allows(t, bootstrapDoc, c.action, c.arn, creating(sibling)) {
				t.Errorf("the %s bootstrap credential can %s tagged for the %s tier", tier, c.action, sibling)
			}
		}
		retag := taggedWithTier(tier, requestContext{"aws:RequestTag/" + naming.EnvTierTagKey: string(sibling)})
		for _, c := range []struct{ action, arn string }{{"iam:TagRole", role}} {
			if allows(t, bootstrapDoc, c.action, c.arn, retag) {
				t.Errorf("the %s bootstrap credential can %s on its own %s as the %s tier's", tier, c.action, c.arn, sibling)
			}
		}
	}
}

func TestABootstrapCredentialReachesTheNamedBootstrapResourcesOfOnlyItsOwnTier(t *testing.T) {
	const account = "us-east-1:111122223333"
	named := func(tier environment.Tier) map[string]string {
		part := defaultNamespace.partStackName(CloudflareEdgePart, tier)
		core, _ := defaultNamespace.StackNameFor(tier)
		edgeUser, _ := defaultNamespace.EdgeUserNameFor(tier)
		return map[string]string{
			"cloudformation:DeleteStack":     "arn:aws:cloudformation:" + account + ":stack/" + core + "/0a1b",
			"cloudformation:CreateChangeSet": "arn:aws:cloudformation:" + account + ":stack/" + part + "/0a1b",
			"iam:PutUserPolicy":              "arn:aws:iam::111122223333:user/" + edgeUser,
			"lambda:PublishLayerVersion":     "arn:aws:lambda:" + account + ":layer:" + runtimeLayerName(defaultNamespace, tier, arch.ARM64, "0123456789abcdef"),
		}
	}
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		bootstrapDoc, _ := renderedCredentialsOf(t, tier)
		own, other := named(tier), named(sibling)
		for action, arn := range own {
			if !allows(t, bootstrapDoc, action, arn, nil) {
				t.Errorf("the %s bootstrap credential cannot %s on %s, its own", tier, action, arn)
			}
			if tier == environment.TierPreview && allows(t, bootstrapDoc, action, other[action], nil) {
				t.Errorf("the preview bootstrap credential can %s on %s, the production tier's", action, other[action])
			}
		}
	}
}

func TestACredentialReachesTheParametersOfOnlyItsOwnTier(t *testing.T) {
	const account = "arn:aws:ssm:us-east-1:111122223333:parameter"
	named := func(tier environment.Tier) []string {
		origin, _ := defaultNamespace.OriginSecretParamFor(tier)
		names := []string{origin}
		for _, kind := range edgeKinds() {
			prefix, _ := defaultNamespace.EdgeParamPrefix(tier, kind)
			names = append(names, prefix+"/credentials")
		}
		return names
	}
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		bootstrapDoc, deployDoc := renderedCredentialsOf(t, tier)
		for _, c := range []struct{ purpose, document, action string }{
			{"bootstrap", bootstrapDoc, "ssm:PutParameter"},
			{"bootstrap", bootstrapDoc, "ssm:DeleteParameter"},
			{"bootstrap", bootstrapDoc, "ssm:GetParameter"},
			{"deploy", deployDoc, "ssm:GetParameter"},
		} {
			for _, name := range named(tier) {
				if !allows(t, c.document, c.action, account+name, nil) {
					t.Errorf("the %s %s credential cannot %s on %s, its own", tier, c.purpose, c.action, name)
				}
			}
			for _, name := range named(sibling) {
				if allows(t, c.document, c.action, account+name, nil) {
					t.Errorf("the %s %s credential can %s on %s, the %s tier's", tier, c.purpose, c.action, name, sibling)
				}
			}
		}
	}
}

func TestABootstrapCredentialMakesTheEdgeInvokeRoleOfOnlyItsOwnTier(t *testing.T) {
	role := func(tier environment.Tier) string {
		return "arn:aws:iam::111122223333:role/" + defaultNamespace.EdgeInvokeRoleName(tier)
	}
	creating := func(tier environment.Tier) requestContext {
		return requestContext{"aws:RequestTag/" + naming.EnvTierTagKey: string(tier)}
	}
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		bootstrapDoc, _ := renderedCredentialsOf(t, tier)
		if !allows(t, bootstrapDoc, "iam:CreateRole", role(tier), creating(tier)) {
			t.Errorf("the %s bootstrap credential cannot create %s, which the core declares", tier, role(tier))
		}
		if !allows(t, bootstrapDoc, "iam:PutRolePolicy", role(tier), taggedWithTier(tier, nil)) {
			t.Errorf("the %s bootstrap credential cannot write the policy of %s", tier, role(tier))
		}
		if allows(t, bootstrapDoc, "iam:PutRolePolicy", role(sibling), taggedWithTier(sibling, nil)) {
			t.Errorf("the %s bootstrap credential can write the policy of %s, the %s tier's", tier, role(sibling), sibling)
		}
	}
}

func TestABootstrapCredentialMakesAndTagsVariablesKeysOnlyAsItsOwnTiers(t *testing.T) {
	creating := func(tier environment.Tier) requestContext {
		return requestContext{
			"aws:RequestTag/" + VariablesKeyComponentTagKey: VariablesKeyComponentTagValue,
			"aws:RequestTag/" + naming.EnvTierTagKey:        string(tier),
		}
	}
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		bootstrapDoc, _ := renderedCredentialsOf(t, tier)
		if !allows(t, bootstrapDoc, "kms:CreateKey", iam.AnyResource, creating(tier)) {
			t.Errorf("the %s bootstrap credential cannot create its own tier's variables key", tier)
		}
		if allows(t, bootstrapDoc, "kms:CreateKey", iam.AnyResource, creating(sibling)) {
			t.Errorf("the %s bootstrap credential can create a variables key tagged for the %s tier", tier, sibling)
		}
		retag := variablesKeyOf(tier)
		retag["aws:RequestTag/"+naming.EnvTierTagKey] = string(sibling)
		if allows(t, bootstrapDoc, "kms:TagResource", variablesKeyARN, retag) {
			t.Errorf("the %s bootstrap credential can retag its own variables key as the %s tier's", tier, sibling)
		}
	}
}

func TestEveryCredentialReadsTheTagsOfTheObjectsItUploadsToTheArtifactBucket(t *testing.T) {
	for _, tier := range bothTiers {
		bucket, err := defaultNamespace.BootstrapBucketName(tier, naming.StorageArtifacts, "abc123")
		if err != nil {
			t.Fatal(err)
		}
		object := "arn:aws:s3:::" + bucket + "/fn/shop/preview/app/r1a2b3c4d/fn-app-bundle-0/h.zip"
		for purpose, document := range credentialsOfTier(t, tier) {
			if !reaches(t, document, "s3:GetObjectTagging", object) {
				t.Errorf("the %s %s credential cannot read the tags of %s, so the provider's read after the upload is refused", tier, purpose, object)
			}
		}
	}
}

func TestTheBootstrapCredentialTurnsABACOnAndTagsItsOwnTiersBuckets(t *testing.T) {
	for _, tier := range bothTiers {
		bootstrapDoc, _ := renderedCredentialsOf(t, tier)
		for _, class := range bootstrapStorageClasses {
			own, err := defaultNamespace.BootstrapBucketName(tier, class, "abc123")
			if err != nil {
				t.Fatal(err)
			}
			other, err := defaultNamespace.BootstrapBucketName(tier.Sibling(), class, "abc123")
			if err != nil {
				t.Fatal(err)
			}
			for _, action := range []string{"s3:PutBucketAbac", "s3:TagResource", "s3:UntagResource", "s3:ListTagsForResource"} {
				if !reaches(t, bootstrapDoc, action, "arn:aws:s3:::"+own) {
					t.Errorf("the %s bootstrap credential cannot %s on %s: CloudFormation tags a bucket with TagResource, the only tagging call S3 takes once ABAC is on", tier, action, own)
				}
				if reaches(t, bootstrapDoc, action, "arn:aws:s3:::"+other) {
					t.Errorf("the %s bootstrap credential can %s on %s, the %s tier's bucket", tier, action, other, tier.Sibling())
				}
			}
			if reaches(t, bootstrapDoc, "s3:PutBucketTagging", "arn:aws:s3:::"+own) {
				t.Errorf("the %s bootstrap credential grants s3:PutBucketTagging on %s, which S3 refuses once ABAC is on", tier, own)
			}
		}
	}
}

func TestNoCredentialTagsAnObjectInTheEdgeBucket(t *testing.T) {
	bucket, err := defaultNamespace.BootstrapBucketName(environment.TierProduction, naming.StorageEdge, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	object := "arn:aws:s3:::" + bucket + "/assets/shop/prod/web/r1a2b3c4d/app.js"
	for purpose, document := range credentialsOfTier(t, environment.TierProduction) {
		if reaches(t, document, "s3:PutObjectTagging", object) {
			t.Errorf("the %s credential can tag %s: an edge reads what sits in the edge bucket, so nothing tags an object for it", purpose, object)
		}
	}
}

type tierResource struct {
	component, action string
	arn               func(environment.Tier) string
	request           func(environment.Tier) requestContext
}

const tierResourceAccount = "us-east-1:111122223333"

func tierOf(purpose string) environment.Tier {
	_, tier, _ := strings.Cut(purpose, " ")
	return environment.Tier(tier)
}

func ocelBucketOf(tier environment.Tier) requestContext {
	return bucketRequest(bucketTaggedFor(tier))
}

func tierCluster(tier environment.Tier) string {
	return "arn:aws:rds:" + tierResourceAccount + ":cluster:ocel-app-" + naming.TierWord(tier) + "-shop-prod-main-k3x9q0"
}

func tierResources() []tierResource {
	const region = tierResourceAccount
	fixed := func(arn string) func(environment.Tier) string { return func(environment.Tier) string { return arn } }
	untagged := func(environment.Tier) requestContext { return nil }
	signingKey := func(tier environment.Tier) string {
		return "arn:aws:secretsmanager:" + region + ":secret:" + realtime.SigningKeySecret(defaultNamespace.SigningKeyRoot(), tier, "shop", "prod", "realtime--live") + "-AbCdEf"
	}
	return []tierResource{
		{"postgres", "rds:ModifyDBCluster", tierCluster, ocelRoleOf},
		{"postgres", "rds:DeleteDBCluster", tierCluster, ocelRoleOf},
		{"postgres", "rds-data:ExecuteStatement", tierCluster, ocelRoleOf},
		{"postgres", "secretsmanager:GetSecretValue", fixed("arn:aws:secretsmanager:" + region + ":secret:rds!cluster-0a1b2c3d-AbCdEf"), func(tier environment.Tier) requestContext {
			return requestContext{"aws:ResourceTag/" + iam.ManagedSecretClusterTagKey: tierCluster(tier)}
		}},
		{"postgres", "rds-db:connect", func(tier environment.Tier) string {
			return "arn:aws:rds-db:" + region + ":dbuser:cluster-ABCDEFGHIJKL/" + postgres.AppRoleFor(tier)
		}, untagged},
		{"kv", "elasticache:DeleteReplicationGroup", fixed("arn:aws:elasticache:" + region + ":replicationgroup:ocel-app-shop-prod-kv-cache"), ocelRoleOf},
		{"kv", "elasticache:ModifyReplicationGroup", fixed("arn:aws:elasticache:" + region + ":replicationgroup:ocel-app-shop-prod-kv-cache"), ocelRoleOf},
		{"kv", "ssm:GetParameter", func(tier environment.Tier) string {
			return "arn:aws:ssm:" + region + ":parameter" + kv.TokenParameter(defaultNamespace.KVTokenRoot(), tier, "shop", "prod", "kv--cache")
		}, untagged},
		{"realtime", "secretsmanager:GetSecretValue", signingKey, ocelRoleOf},
		{"realtime", "secretsmanager:DeleteSecret", signingKey, ocelRoleOf},
		{"realtime", "appsync:DeleteApi", fixed("arn:aws:appsync:" + region + ":apis/abcdefghijklmnopqrstuvwxyz"), ocelRoleOf},
		{"bucket", "s3:DeleteBucket", fixed(appBucketARN), ocelBucketOf},
		{"bucket", "s3:PutBucketNotification", fixed(appBucketARN), ocelBucketOf},
		{"topic", "sqs:DeleteQueue", fixed("arn:aws:sqs:" + region + ":ocel-app-shop-prod-topic-orders"), ocelRoleOf},
		{"topic", "sqs:SetQueueAttributes", fixed("arn:aws:sqs:" + region + ":ocel-app-shop-prod-topic-orders"), ocelRoleOf},
		{"topic", "sns:DeleteTopic", fixed("arn:aws:sns:" + region + ":ocel-app-shop-prod-topic-orders"), ocelRoleOf},
		{"topic", "sns:Subscribe", fixed("arn:aws:sns:" + region + ":ocel-app-shop-prod-topic-orders"), ocelRoleOf},
		{"task", "scheduler:DeleteScheduleGroup", fixed("arn:aws:scheduler:" + region + ":schedule-group/ocel-app-shop-prod-heartbeat"), ocelRoleOf},
	}
}

func TestADeployCredentialReachesTheResourcesOfOnlyItsOwnTier(t *testing.T) {
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		_, deployDoc := renderedCredentialsOf(t, tier)
		for _, r := range tierResources() {
			if !allows(t, deployDoc, r.action, r.arn(tier), r.request(tier)) {
				t.Errorf("the %s deploy credential cannot %s on its own %s resource %s", tier, r.action, r.component, r.arn(tier))
			}
			if allows(t, deployDoc, r.action, r.arn(sibling), r.request(sibling)) {
				t.Errorf("the %s deploy credential can %s on %s, a %s resource of the %s tier", tier, r.action, r.arn(sibling), r.component, sibling)
			}
		}
	}
}

func TestADeployCredentialCreatesAndRetagsResourcesOnlyForItsOwnTier(t *testing.T) {
	const region = tierResourceAccount
	creating := func(tier environment.Tier) requestContext {
		return requestContext{"aws:RequestTag/" + iam.ManagedByTagKey: string(defaultNamespace), "aws:RequestTag/" + naming.EnvTierTagKey: string(tier)}
	}
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		_, deployDoc := renderedCredentialsOf(t, tier)
		for _, c := range []struct{ action, arn string }{
			{"rds:CreateDBCluster", tierCluster(tier)},
			{"elasticache:CreateReplicationGroup", "arn:aws:elasticache:" + region + ":replicationgroup:ocel-app-shop-prod-kv-cache"},
			{"appsync:CreateApi", iam.AnyResource},
			{"sqs:CreateQueue", "arn:aws:sqs:" + region + ":ocel-app-shop-prod-topic-orders"},
			{"sns:CreateTopic", "arn:aws:sns:" + region + ":ocel-app-shop-prod-topic-orders"},
			{"scheduler:CreateScheduleGroup", "arn:aws:scheduler:" + region + ":schedule-group/ocel-app-shop-prod-heartbeat"},
		} {
			if !allows(t, deployDoc, c.action, c.arn, creating(tier)) {
				t.Errorf("the %s deploy credential cannot %s tagged for its own tier", tier, c.action)
			}
			if allows(t, deployDoc, c.action, c.arn, creating(sibling)) {
				t.Errorf("the %s deploy credential can %s tagged for the %s tier", tier, c.action, sibling)
			}
		}
		retag := ocelRoleOf(sibling)
		maps.Copy(retag, creating(tier))
		for _, c := range []struct{ action, arn string }{
			{"rds:AddTagsToResource", tierCluster(sibling)},
			{"elasticache:AddTagsToResource", "arn:aws:elasticache:" + region + ":replicationgroup:ocel-app-shop-prod-kv-cache"},
			{"sqs:TagQueue", "arn:aws:sqs:" + region + ":ocel-app-shop-prod-topic-orders"},
			{"sns:TagResource", "arn:aws:sns:" + region + ":ocel-app-shop-prod-topic-orders"},
			{"appsync:TagResource", "arn:aws:appsync:" + region + ":apis/abcdefghijklmnopqrstuvwxyz"},
			{"scheduler:TagResource", "arn:aws:scheduler:" + region + ":schedule-group/ocel-app-shop-prod-heartbeat"},
		} {
			if allows(t, deployDoc, c.action, c.arn, retag) {
				t.Errorf("the %s deploy credential can %s on %s, the %s tier's, retagging it as its own", tier, c.action, c.arn, sibling)
			}
		}
	}
}
