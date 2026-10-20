package account

import (
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/taginvalidator"
)

func TestTheDeployCredentialAnswersAndShieldsThePublicFrontOnlyOnWhatItTagged(t *testing.T) {
	grants := grantsOf(t, mustRender(t, DeployCredentialPermissions))
	want := []grant{
		{action: "elasticloadbalancing:AddListenerCertificates", resource: productionScope.ContainerListenerARNs()[1], condition: conditionJSON(t, productionScope.TaggedByOcelForItsTier())},
		{action: "elasticloadbalancing:RemoveListenerCertificates", resource: productionScope.ContainerListenerARNs()[1], condition: conditionJSON(t, productionScope.TaggedByOcelForItsTier())},
		{action: "elasticloadbalancing:CreateTrustStore", resource: productionScope.ContainerTrustStoreARN(), condition: conditionJSON(t, productionScope.TaggedOnCreateForItsTier())},
		{action: "elasticloadbalancing:ModifyTrustStore", resource: productionScope.ContainerTrustStoreARN(), condition: conditionJSON(t, productionScope.TaggedByOcelForItsTier())},
		{action: "elasticloadbalancing:AddTags", resource: productionScope.ContainerTrustStoreARN(), condition: conditionJSON(t, productionScope.TaggedByOcelForItsTier())},
	}
	for _, wanted := range want {
		if !grants[wanted] {
			t.Errorf("the deploy credential does not grant %s on %s under %s: a hostname forwarded to the public load balancer needs its certificate attached and the edge's client certificate trusted", wanted.action, wanted.resource, wanted.condition)
		}
	}
}

func TestEveryCredentialChangesOnlyTheCertificatesItTagged(t *testing.T) {
	for purpose, document := range bothCredentials(t) {
		scope := defaultNamespace.Scope(tierOf(purpose))
		want := map[grant]bool{
			{action: "acm:ImportCertificate", resource: iam.AppCertificateARN, condition: conditionJSON(t, scope.TaggedOnCreateForItsTier())}: true,
			{action: "acm:RequestCertificate", resource: iam.AnyResource, condition: conditionJSON(t, scope.TaggedOnCreateForItsTier())}:      true,
			{action: "acm:AddTagsToCertificate", resource: iam.AppCertificateARN, condition: conditionJSON(t, map[string]any{
				"StringEquals": map[string]any{
					"aws:RequestTag/ocel:managed-by": "ocel",
					"aws:RequestTag/ocel:env-tier":   string(scope.Tier),
				},
				"StringEqualsIfExists": map[string]any{"acm:CertificateKeyPairOrigin": "CUSTOMER_PROVIDED"},
			})}: true,
			{action: "acm:AddTagsToCertificate", resource: iam.AppCertificateARN, condition: conditionJSON(t, scope.TaggedByOcelForItsTier())}:         true,
			{action: "acm:RemoveTagsFromCertificate", resource: iam.AppCertificateARN, condition: conditionJSON(t, scope.TaggedByOcelForItsTier())}:    true,
			{action: "acm:DeleteCertificate", resource: iam.AppCertificateARN, condition: conditionJSON(t, scope.TaggedByOcelForItsTier())}:            true,
			{action: "acm:DescribeCertificate", resource: iam.AppCertificateARN, condition: conditionJSON(t, scope.UnevaluatedOrMadeByItsStacks())}:    true,
			{action: "acm:ListTagsForCertificate", resource: iam.AppCertificateARN, condition: conditionJSON(t, scope.UnevaluatedOrMadeByItsStacks())}: true,
			{action: "acm:ListCertificates", resource: iam.AnyResource, condition: conditionJSON(t, nil)}:                                              true,
		}
		granted := map[grant]bool{}
		for g := range grantsOf(t, document) {
			if strings.HasPrefix(g.action, "acm:") {
				granted[g] = true
			}
		}
		for wanted := range want {
			if !granted[wanted] {
				t.Errorf("the %s credential does not grant %s on %s under %s: the public load balancer's default certificate is imported into ACM, and every custom hostname's is requested there", purpose, wanted.action, wanted.resource, wanted.condition)
			}
		}
		for g := range granted {
			if !want[g] {
				t.Errorf("the %s credential grants %s on %s under %s, which reaches certificates Ocel never imported or requested", purpose, g.action, g.resource, g.condition)
			}
		}
	}
}

func TestEveryCredentialGrantsEveryACMCallACustomHostnameCertificateMakes(t *testing.T) {
	calls := reflect.TypeFor[certs.ACMAPI]()
	for purpose, document := range bothCredentials(t) {
		granted := map[string]bool{}
		for g := range grantsOf(t, document) {
			granted[g.action] = true
		}
		for i := range calls.NumMethod() {
			if action := "acm:" + calls.Method(i).Name; !granted[action] {
				t.Errorf("the %s credential does not grant %s, which certs.ACM calls for every custom hostname", purpose, action)
			}
		}
	}
}

func TestEveryCredentialRequestsAndDeletesOnlyTheCertificatesOcelTagged(t *testing.T) {
	const certificate = "arn:aws:acm:us-east-1:111122223333:certificate/abcd-1234"
	for purpose, document := range bothCredentials(t) {
		tier := string(tierOf(purpose))
		sibling := string(tierOf(purpose).Sibling())
		requestedByOcel := requestContext{"aws:RequestTag/ocel:managed-by": "ocel", "aws:RequestTag/ocel:env-tier": tier, "acm:CertificateKeyPairOrigin": "AWS_MANAGED"}
		cases := []struct {
			name     string
			action   string
			resource string
			request  requestContext
			allowed  bool
		}{
			{"requests a certificate tagged as Ocel's", "acm:RequestCertificate", iam.AnyResource, requestedByOcel, true},
			{"requests a certificate with no Ocel tag", "acm:RequestCertificate", iam.AnyResource, requestContext{"acm:CertificateKeyPairOrigin": "AWS_MANAGED"}, false},
			{"requests a certificate tagged for the other tier", "acm:RequestCertificate", iam.AnyResource, requestContext{
				"aws:RequestTag/ocel:managed-by": "ocel",
				"aws:RequestTag/ocel:env-tier":   sibling,
				"acm:CertificateKeyPairOrigin":   "AWS_MANAGED",
			}, false},
			{"tags the certificate it requests with Ocel's tag", "acm:AddTagsToCertificate", certificate, requestContext{
				"aws:RequestTag/ocel:managed-by":  "ocel",
				"aws:RequestTag/ocel:env-tier":    tier,
				"aws:ResourceTag/ocel:managed-by": "ocel",
				"aws:ResourceTag/ocel:env-tier":   tier,
				"acm:CertificateKeyPairOrigin":    "AWS_MANAGED",
			}, true},
			{"tags an ACM-issued certificate Ocel never requested", "acm:AddTagsToCertificate", certificate, requestedByOcel, false},
			{"describes a certificate the account made itself", "acm:DescribeCertificate", certificate, requestContext{}, true},
			{"lists the account's issued certificates", "acm:ListCertificates", iam.AnyResource, requestContext{}, true},
			{"deletes an ACM-issued certificate Ocel requested", "acm:DeleteCertificate", certificate, requestContext{
				"aws:ResourceTag/ocel:managed-by": "ocel",
				"aws:ResourceTag/ocel:env-tier":   tier,
				"acm:CertificateKeyPairOrigin":    "AWS_MANAGED",
			}, true},
			{"deletes an ACM-issued certificate the other tier requested", "acm:DeleteCertificate", certificate, requestContext{
				"aws:ResourceTag/ocel:managed-by": "ocel",
				"aws:ResourceTag/ocel:env-tier":   sibling,
				"acm:CertificateKeyPairOrigin":    "AWS_MANAGED",
			}, false},
			{"deletes an ACM-issued certificate Ocel never requested", "acm:DeleteCertificate", certificate, requestContext{"acm:CertificateKeyPairOrigin": "AWS_MANAGED"}, false},
			{"deletes a certificate the account imported itself", "acm:DeleteCertificate", certificate, requestContext{"acm:CertificateKeyPairOrigin": "CUSTOMER_PROVIDED"}, false},
		}
		for _, c := range cases {
			if got := allows(t, document, c.action, c.resource, c.request); got != c.allowed {
				t.Errorf("the %s credential %s: allowed = %t, want %t", purpose, c.name, got, c.allowed)
			}
		}
	}
}

func tagInvalidatorRoleOf(tier environment.Tier) string {
	return "arn:aws:iam::111122223333:role" + defaultNamespace.bootstrapRolePathFor(tier) + taginvalidator.NamesFor(string(defaultNamespace), tier).Role
}

func tagInvalidatorFunctionOf(tier environment.Tier) string {
	return "arn:aws:lambda:us-east-1:111122223333:function:" + taginvalidator.NamesFor(string(defaultNamespace), tier).Function
}

func TestBothCredentialsPassTheTagInvalidatorRoleOfOnlyTheirOwnTierToLambda(t *testing.T) {
	toLambda := requestContext{"iam:PassedToService": iam.LambdaPrincipal}
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		for purpose, document := range credentialsOfTier(t, tier) {
			if !allows(t, document, "iam:PassRole", tagInvalidatorRoleOf(tier), toLambda) {
				t.Errorf("the %s %s credential cannot pass %s to Lambda, so the first deploy that revalidates cannot make the invalidator", tier, purpose, tagInvalidatorRoleOf(tier))
			}
			if allows(t, document, "iam:PassRole", tagInvalidatorRoleOf(sibling), toLambda) {
				t.Errorf("the %s %s credential can pass %s, the %s tier's invalidator role", tier, purpose, tagInvalidatorRoleOf(sibling), sibling)
			}
		}
	}
}

func TestBothCredentialsMapTheStreamOntoTheTagInvalidatorOfOnlyTheirOwnTier(t *testing.T) {
	const mapping = "arn:aws:lambda:us-east-1:111122223333:event-source-mapping:1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d"
	invoking := func(function string) requestContext { return requestContext{"lambda:FunctionArn": function} }
	for _, tier := range bothTiers {
		own, other := tagInvalidatorFunctionOf(tier), tagInvalidatorFunctionOf(tier.Sibling())
		for purpose, document := range credentialsOfTier(t, tier) {
			for _, function := range []string{own, own + ":$LATEST"} {
				if !allows(t, document, "lambda:CreateEventSourceMapping", "*", invoking(function)) {
					t.Errorf("the %s %s credential cannot map the stream onto %s; Lambda authorizes the function name and qualifier the call names", tier, purpose, function)
				}
				if !allows(t, document, "lambda:UpdateEventSourceMapping", mapping, invoking(function)) {
					t.Errorf("the %s %s credential cannot update the mapping onto %s", tier, purpose, function)
				}
			}
			for _, function := range []string{other, other + ":$LATEST"} {
				if allows(t, document, "lambda:CreateEventSourceMapping", "*", invoking(function)) {
					t.Errorf("the %s %s credential can map a stream onto %s, the %s tier's invalidator", tier, purpose, function, tier.Sibling())
				}
			}
		}
	}
}

func TestBothCredentialsUpdateOnlyTheirOwnTiersEdgeFunctionsAndOnlyTheBootstrapCredentialDeletesACachePolicy(t *testing.T) {
	resolverOf := func(tier environment.Tier) string {
		return "arn:aws:cloudfront::111122223333:function/" + defaultNamespace.EdgeResolverName(tier)
	}
	const cachePolicy = "arn:aws:cloudfront::111122223333:cache-policy/658327ea-f89d-4fab-a63d-7e88639e58f6"
	for _, tier := range bothTiers {
		bootstrapDoc, deployDoc := renderedCredentialsOf(t, tier)
		for purpose, document := range map[string]string{"bootstrap": bootstrapDoc, "deploy": deployDoc} {
			if !allows(t, document, "cloudfront:UpdateFunction", resolverOf(tier), nil) {
				t.Errorf("the %s %s credential cannot update %s, its own tier's resolver", tier, purpose, resolverOf(tier))
			}
			if allows(t, document, "cloudfront:UpdateFunction", resolverOf(tier.Sibling()), nil) {
				t.Errorf("the %s %s credential can update %s, the %s tier's resolver", tier, purpose, resolverOf(tier.Sibling()), tier.Sibling())
			}
		}
		if !allows(t, bootstrapDoc, "cloudfront:DeleteCachePolicy", cachePolicy, nil) {
			t.Errorf("the %s bootstrap credential cannot delete the cache policy a removal reclaims", tier)
		}
		if allows(t, deployDoc, "cloudfront:DeleteCachePolicy", cachePolicy, nil) {
			t.Errorf("the %s deploy credential can delete a cache policy, which every CloudFront deploy of the tier shares", tier)
		}
	}
}

func TestTheDeployCredentialCreatesARestAPIOnlyTaggedAsOcelsAndItsOwnTiers(t *testing.T) {
	const restAPIs = "arn:aws:apigateway:us-east-1::/restapis"
	tagging := func(managedBy, tier string) requestContext {
		request := requestContext{}
		if managedBy != "" {
			request["aws:RequestTag/"+naming.ManagedByTagKey] = managedBy
		}
		if tier != "" {
			request["aws:RequestTag/"+naming.EnvTierTagKey] = tier
		}
		return request
	}
	for _, tier := range bothTiers {
		_, deployDoc := renderedCredentialsOf(t, tier)
		if !allows(t, deployDoc, "apigateway:POST", restAPIs, tagging(string(defaultNamespace), string(tier))) {
			t.Errorf("the %s deploy credential cannot create a REST API tagged as Ocel's and its own tier's", tier)
		}
		for name, request := range map[string]requestContext{
			"untagged":                     tagging("", ""),
			"without the managed-by tag":   tagging("", string(tier)),
			"without the tier tag":         tagging(string(defaultNamespace), ""),
			"tagged as the sibling tier's": tagging(string(defaultNamespace), string(tier.Sibling())),
		} {
			if allows(t, deployDoc, "apigateway:POST", restAPIs, request) {
				t.Errorf("the %s deploy credential can create a REST API %s", tier, name)
			}
		}
	}
}

func TestTheDeployCredentialManagesOnlyTheDistributionsOcelTagsForItsTier(t *testing.T) {
	const distribution = "arn:aws:cloudfront::111122223333:distribution/E2QWRUHAPOMQZL"
	tags := func(prefix, managedBy, tier string) requestContext {
		request := requestContext{}
		if managedBy != "" {
			request[prefix+naming.ManagedByTagKey] = managedBy
		}
		if tier != "" {
			request[prefix+naming.EnvTierTagKey] = tier
		}
		return request
	}
	onExisting := []string{
		"cloudfront:DeleteDistribution",
		"cloudfront:GetDistribution",
		"cloudfront:GetDistributionConfig",
		"cloudfront:UpdateDistribution",
	}
	for _, tier := range bothTiers {
		_, deployDoc := renderedCredentialsOf(t, tier)
		sibling := string(tier.Sibling())
		if !allows(t, deployDoc, "cloudfront:ListDistributions", iam.AnyResource, nil) {
			t.Errorf("the %s deploy credential cannot list the distributions it finds its own among", tier)
		}
		created := tags("aws:RequestTag/", string(defaultNamespace), string(tier))
		if !allows(t, deployDoc, "cloudfront:CreateDistribution", iam.AnyResource, created) {
			t.Errorf("the %s deploy credential cannot create a distribution tagged as Ocel's and its own tier's", tier)
		}
		if !allows(t, deployDoc, "cloudfront:TagResource", distribution, created) {
			t.Errorf("the %s deploy credential cannot tag the distribution it creates as Ocel's and its own tier's", tier)
		}
		for name, request := range map[string]requestContext{
			"untagged":                     tags("aws:RequestTag/", "", ""),
			"without the managed-by tag":   tags("aws:RequestTag/", "", string(tier)),
			"without the tier tag":         tags("aws:RequestTag/", string(defaultNamespace), ""),
			"tagged as the sibling tier's": tags("aws:RequestTag/", string(defaultNamespace), sibling),
		} {
			if allows(t, deployDoc, "cloudfront:CreateDistribution", iam.AnyResource, request) {
				t.Errorf("the %s deploy credential can create a distribution %s", tier, name)
			}
			if allows(t, deployDoc, "cloudfront:TagResource", distribution, request) {
				t.Errorf("the %s deploy credential can tag a distribution %s", tier, name)
			}
		}
		retagged := tags("aws:ResourceTag/", string(defaultNamespace), sibling)
		maps.Copy(retagged, created)
		if allows(t, deployDoc, "cloudfront:TagResource", distribution, retagged) {
			t.Errorf("the %s deploy credential can retag the %s tier's distribution as its own", tier, sibling)
		}
		for _, action := range onExisting {
			if !allows(t, deployDoc, action, distribution, tags("aws:ResourceTag/", string(defaultNamespace), string(tier))) {
				t.Errorf("the %s deploy credential cannot call %s on a distribution Ocel tagged for its tier", tier, action)
			}
			for name, request := range map[string]requestContext{
				"untagged":                     tags("aws:ResourceTag/", "", ""),
				"not tagged as Ocel's":         tags("aws:ResourceTag/", "", string(tier)),
				"tagged as the sibling tier's": tags("aws:ResourceTag/", string(defaultNamespace), sibling),
			} {
				if allows(t, deployDoc, action, distribution, request) {
					t.Errorf("the %s deploy credential can call %s on a distribution %s", tier, action, name)
				}
			}
		}
	}
}

func TestEveryCredentialCreatesTheContainerFrontsVPCOriginOnlyTaggedAsOcels(t *testing.T) {
	const vpcOrigin = "arn:aws:cloudfront::111122223333:vpcorigin/vo_0123456789abcdef"
	tagged := func(managedBy string, tier environment.Tier) requestContext {
		if managedBy == "" {
			return requestContext{}
		}
		return requestContext{"aws:RequestTag/" + naming.ManagedByTagKey: managedBy, "aws:RequestTag/" + naming.EnvTierTagKey: string(tier)}
	}
	onCreate := map[string]string{
		"cloudfront:CreateVpcOrigin": iam.AnyResource,
		"cloudfront:TagResource":     vpcOrigin,
	}
	for purpose, document := range bothCredentials(t) {
		tier := tierOf(purpose)
		for action, resource := range onCreate {
			if !allows(t, document, action, resource, tagged(string(defaultNamespace), tier)) {
				t.Errorf("the %s credential cannot call %s on %q for a VPC origin it creates tagged as Ocel's", purpose, action, resource)
			}
			for name, request := range map[string]requestContext{
				"untagged":                  tagged("", tier),
				"tagged as someone else's":  tagged("someone-else", tier),
				"tagged for the other tier": tagged(string(defaultNamespace), tier.Sibling()),
			} {
				if allows(t, document, action, resource, request) {
					t.Errorf("the %s credential can call %s on %q for a VPC origin %s", purpose, action, resource, name)
				}
			}
		}
	}
}

func TestEveryCredentialReadsTheAvailabilityZonesTheContainerFrontsVPCOriginReaches(t *testing.T) {
	for purpose, document := range bothCredentials(t) {
		if !allows(t, document, "ec2:DescribeAvailabilityZones", iam.AnyResource, requestContext{}) {
			t.Errorf("the %s credential cannot call ec2:DescribeAvailabilityZones, so the container infra cannot keep its load balancers out of the zones a VPC origin refuses", purpose)
		}
	}
}

func TestEveryCredentialFindsAndRemovesOnlyItsOwnTiersFailedVPCOrigins(t *testing.T) {
	const vpcOrigin = "arn:aws:cloudfront::111122223333:vpcorigin/vo_0123456789abcdef"
	tagged := func(managedBy string, tier environment.Tier) requestContext {
		return requestContext{"aws:ResourceTag/" + naming.ManagedByTagKey: managedBy, "aws:ResourceTag/" + naming.EnvTierTagKey: string(tier)}
	}
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		bootstrapDoc, deployDoc := renderedCredentialsOf(t, tier)
		for purpose, document := range map[string]string{"bootstrap": bootstrapDoc, "deploy": deployDoc} {
			if !allows(t, document, "cloudfront:ListVpcOrigins", iam.AnyResource, requestContext{}) {
				t.Errorf("the %s %s credential cannot call cloudfront:ListVpcOrigins, so a retried deploy cannot find the VPC origin a failed one left behind", tier, purpose)
			}
			for _, action := range []string{"cloudfront:GetVpcOrigin", "cloudfront:DeleteVpcOrigin", "cloudfront:ListTagsForResource"} {
				if !allows(t, document, action, vpcOrigin, tagged(string(defaultNamespace), tier)) {
					t.Errorf("the %s %s credential cannot call %s on a VPC origin Ocel tagged for its tier", tier, purpose, action)
				}
				for name, request := range map[string]requestContext{
					"untagged":                     {},
					"tagged as another install's":  tagged("someone-else", tier),
					"tagged as the sibling tier's": tagged(string(defaultNamespace), tier.Sibling()),
				} {
					if allows(t, document, action, vpcOrigin, request) {
						t.Errorf("the %s %s credential can call %s on a VPC origin %s", tier, purpose, action, name)
					}
				}
			}
		}
	}
}

func TestEveryCredentialFindsRestAPIsByTagAndTagsOnlyItsOwnTiers(t *testing.T) {
	const restAPITags = "arn:aws:apigateway:eu-west-1::/tags/arn%3Aaws%3Aapigateway%3Aeu-west-1%3A%3A%2Frestapis%2Fabc123"
	creating := func(tier environment.Tier) requestContext {
		return requestContext{
			"aws:RequestTag/" + naming.ManagedByTagKey: string(defaultNamespace),
			"aws:RequestTag/" + naming.EnvTierTagKey:   string(tier),
		}
	}
	for _, tier := range bothTiers {
		for purpose, document := range credentialsOfTier(t, tier) {
			if !allows(t, document, "tag:GetResources", iam.AnyResource, requestContext{}) {
				t.Errorf("the %s %s credential cannot find REST APIs by their tags, so the API Gateway edge cannot reach a project's APIs", tier, purpose)
			}
			if !allows(t, document, "apigateway:PUT", restAPITags, creating(tier)) {
				t.Errorf("the %s %s credential cannot tag a REST API it creates as its own tier's", tier, purpose)
			}
			if allows(t, document, "apigateway:PUT", restAPITags, creating(tier.Sibling())) {
				t.Errorf("the %s %s credential can tag a REST API as the %s tier's", tier, purpose, tier.Sibling())
			}
			siblings := creating(tier)
			siblings["aws:ResourceTag/"+naming.ManagedByTagKey] = string(defaultNamespace)
			siblings["aws:ResourceTag/"+naming.EnvTierTagKey] = string(tier.Sibling())
			if allows(t, document, "apigateway:PUT", restAPITags, siblings) {
				t.Errorf("the %s %s credential can retag the %s tier's REST API as its own", tier, purpose, tier.Sibling())
			}
		}
	}
}
