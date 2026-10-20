package account

import (
	"maps"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

const appBucketARN = "arn:aws:s3:::ocel-app-shop-prod-uploads-k3x9q0"

func bucketRequest(also ...requestContext) requestContext {
	request := requestContext{"aws:ResourceAccount": "111122223333", "aws:PrincipalAccount": "111122223333"}
	for _, more := range also {
		maps.Copy(request, more)
	}
	return request
}

func bucketTaggedFor(tier environment.Tier) requestContext {
	return requestContext{
		"aws:ResourceTag/" + naming.ManagedByTagKey: string(defaultNamespace),
		"aws:ResourceTag/" + naming.EnvTierTagKey:   string(tier),
	}
}

func bucketCreatedFor(tier environment.Tier) requestContext {
	return requestContext{
		"aws:RequestTag/" + naming.ManagedByTagKey: string(defaultNamespace),
		"aws:RequestTag/" + naming.EnvTierTagKey:   string(tier),
	}
}

func TestADeployCredentialCreatesAnAppBucketTaggedForOnlyItsOwnTierAndTurnsOnItsABAC(t *testing.T) {
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		_, deployDoc := renderedCredentialsOf(t, tier)
		for _, action := range []string{"s3:CreateBucket", "s3:TagResource"} {
			if !allows(t, deployDoc, action, appBucketARN, bucketRequest(bucketCreatedFor(tier))) {
				t.Errorf("the %s deploy credential cannot %s tagged for its own tier: pulumi-aws creates a bucket with its tags in the one CreateBucket call", tier, action)
			}
			if allows(t, deployDoc, action, appBucketARN, bucketRequest(bucketCreatedFor(sibling))) {
				t.Errorf("the %s deploy credential can %s tagged for the %s tier", tier, action, sibling)
			}
		}
		if !allows(t, deployDoc, "s3:PutBucketAbac", appBucketARN, bucketRequest()) {
			t.Errorf("the %s deploy credential cannot turn on ABAC for a bucket it just made, whose tags S3 does not evaluate until then", tier)
		}
		for _, read := range []string{"s3:ListBucket", "s3:GetBucketPolicy", "s3:GetBucketTagging", "s3:ListTagsForResource", "s3:GetBucketAbac"} {
			if !allows(t, deployDoc, read, appBucketARN, bucketRequest()) {
				t.Errorf("the %s deploy credential cannot %s on a bucket whose ABAC is not on yet: pulumi-aws reads it back before the ABAC resource runs", tier, read)
			}
		}
	}
}

func TestADeployCredentialChangesOnlyTheAppBucketsItsOwnTierTagged(t *testing.T) {
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		_, deployDoc := renderedCredentialsOf(t, tier)
		for _, action := range []string{"s3:DeleteBucket", "s3:PutBucketCORS", "s3:PutBucketNotification", "s3:PutBucketPublicAccessBlock", "s3:UntagResource"} {
			if !allows(t, deployDoc, action, appBucketARN, bucketRequest(bucketTaggedFor(tier))) {
				t.Errorf("the %s deploy credential cannot %s on its own tier's bucket", tier, action)
			}
			if allows(t, deployDoc, action, appBucketARN, bucketRequest(bucketTaggedFor(sibling))) {
				t.Errorf("the %s deploy credential can %s on a bucket the %s tier tagged", tier, action, sibling)
			}
			if allows(t, deployDoc, action, appBucketARN, bucketRequest()) {
				t.Errorf("the %s deploy credential can %s on a bucket whose tier S3 does not evaluate", tier, action)
			}
		}
		for _, action := range []string{"s3:TagResource", "s3:PutBucketAbac"} {
			if allows(t, deployDoc, action, appBucketARN, bucketRequest(bucketTaggedFor(sibling), bucketCreatedFor(tier))) {
				t.Errorf("the %s deploy credential can %s on a bucket the %s tier tagged, taking it over", tier, action, sibling)
			}
		}
		if reaches(t, deployDoc, "s3:PutBucketTagging", appBucketARN) {
			t.Errorf("the %s deploy credential grants s3:PutBucketTagging, which S3 refuses once ABAC is on and which retags a bucket past its tier condition", tier)
		}
	}
}
