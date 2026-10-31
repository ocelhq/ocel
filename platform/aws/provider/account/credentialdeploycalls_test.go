package account

import (
	"maps"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
)

const deployingAccount = "111122223333"

func TestTheDeployCredentialAllowsWhatADeployWasRefusedInAnAccount(t *testing.T) {
	t.Parallel()

	for _, tier := range bothTiers {
		_, deployDoc := renderedCredentialsOf(t, tier)
		ownTier := requestTaggedFor(tier)
		gone := requestTaggedFor(tier)
		delete(gone, "aws:ResourceTag/"+naming.EnvTierTagKey)
		apiGatewayParams, err := defaultNamespace.EdgeParamPrefix(tier, "api-gateway")
		if err != nil {
			t.Fatal(err)
		}
		function := filledIn(t, defaultNamespace.Scope(tier).AppFunctionARN(), ownTier)
		passedToAPIGateway := maps.Clone(ownTier)
		passedToAPIGateway["iam:PassedToService"] = iam.APIGatewayPrincipal
		for _, call := range []struct {
			action, arn string
			request     requestContext
			why         string
		}{
			{"ssm:GetParameters", "arn:aws:ssm:us-east-1:" + deployingAccount + ":parameter" + apiGatewayParams + "/credentials", ownTier,
				"a release reads the parameters of the edge it fronts, the API Gateway edge too, which has no bootstrap part"},
			{"iam:PassRole", "arn:aws:iam::" + deployingAccount + ":role/" + defaultNamespace.EdgeInvokeRoleName(tier), passedToAPIGateway,
				"the API Gateway edge integrates its routes through the role the bootstrap made for it"},
			{"s3:PutObjectTagging", "arn:aws:s3:::" + defaultNamespace.BootstrapBucketName(tier, naming.StorageArtifacts, "abc123") + "/functions/web.zip", ownTier,
				"the engine uploads the artifacts it stages with their tags"},
			{"lambda:ListVersionsByFunction", function, ownTier, "the engine reads a function's versions"},
			{"lambda:GetFunctionCodeSigningConfig", function, ownTier, "the engine reads a function's code signing config"},
			{"lambda:GetFunction", function, gone, "waiting on a deleted function reads one that holds no tag any more"},
			{"cloudformation:DescribeStacks", "arn:aws:cloudformation:us-east-1:" + deployingAccount + ":stack/" + naming.MustUntieredOcelName(string(defaultNamespace), "connector") + "/x",
				ownTier, "a job reads the connector to name the target it reports its deployment against"},
		} {
			if !allows(t, deployDoc, call.action, call.arn, call.request) {
				t.Errorf("the %s deploy credential cannot %s on %s: %s", tier, call.action, call.arn, call.why)
			}
		}
		otherTier := requestTaggedFor(tier.Sibling())
		if allows(t, deployDoc, "lambda:GetFunction", function, otherTier) {
			t.Errorf("the %s deploy credential can read %s tagged for the %s tier", tier, function, tier.Sibling())
		}
	}
}
