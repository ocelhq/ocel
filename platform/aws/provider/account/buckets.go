package account

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
)

const (
	outputStateBucket    = "StateBucketName"
	outputArtifactBucket = "ArtifactBucketName"
	outputEdgeBucket     = "EdgeBucketName"
	outputEdgeBucketARN  = "EdgeBucketArn"
	outputCacheBucket    = "CacheBucketName"

	artifactExpirationDays     = 30
	artifactAbortMultipartDays = 7

	stateNoncurrentDays     = 7
	stateAbortMultipartDays = 7
)

var BootstrapStorageClasses = []naming.StorageClass{naming.StorageState, naming.StorageArtifacts, naming.StorageEdge, naming.StorageCache}

var bucketOutputs = map[string]naming.StorageClass{
	outputStateBucket:    naming.StorageState,
	outputArtifactBucket: naming.StorageArtifacts,
	outputEdgeBucket:     naming.StorageEdge,
	outputCacheBucket:    naming.StorageCache,
}

func (n Namespace) bootstrapBucketTag(tier environment.Tier, buckets naming.Buckets) (string, error) {
	tag := ""
	for _, class := range BootstrapStorageClasses {
		suffix, named := strings.CutPrefix(buckets[class], n.BootstrapBucketStem(tier, class)+naming.WordSeparator)
		if !named || !naming.IsRandomTag(suffix) || (tag != "" && suffix != tag) {
			core, _ := n.StackNameFor(tier)
			return "", refusal.Refuse(refusal.CodeInvalid,
				"stack %s names buckets that do not all end in the one random tag its bootstrap minted, and a new tag would rename and so replace every one of them; delete stack %s in CloudFormation and run the %s bootstrap again",
				core, core, tier)
		}
		tag = suffix
	}
	return tag, nil
}

func (n Namespace) bootstrapBuckets(tier environment.Tier, tag string) naming.Buckets {
	buckets := make(naming.Buckets, len(BootstrapStorageClasses))
	for _, class := range BootstrapStorageClasses {
		buckets[class] = n.BootstrapBucketName(tier, class, tag)
	}
	return buckets
}

func bucketProperties(name string, lifecycle ...any) map[string]any {
	return map[string]any{
		"BucketName":                     name,
		"AbacStatus":                     "Enabled",
		"BucketEncryption":               bucketEncryption(),
		"PublicAccessBlockConfiguration": blockedPublicAccess(),
		"LifecycleConfiguration":         map[string]any{"Rules": lifecycle},
	}
}

func bucketEncryption() map[string]any {
	return map[string]any{
		"ServerSideEncryptionConfiguration": []any{
			map[string]any{"ServerSideEncryptionByDefault": map[string]any{"SSEAlgorithm": "AES256"}},
		},
	}
}

func blockedPublicAccess() map[string]any {
	return map[string]any{
		"BlockPublicAcls":       true,
		"BlockPublicPolicy":     true,
		"IgnorePublicAcls":      true,
		"RestrictPublicBuckets": true,
	}
}

func stateBucketResource(tier environment.Tier, name string) cfn.Resource {
	properties := bucketProperties(name,
		map[string]any{
			"Id":                             "expire-noncurrent-state",
			"Status":                         "Enabled",
			"NoncurrentVersionExpiration":    map[string]any{"NoncurrentDays": stateNoncurrentDays},
			"AbortIncompleteMultipartUpload": map[string]any{"DaysAfterInitiation": stateAbortMultipartDays},
		},
		map[string]any{
			"Id":                        "expire-state-delete-markers",
			"Status":                    "Enabled",
			"ExpiredObjectDeleteMarker": true,
		},
	)
	properties["VersioningConfiguration"] = map[string]any{"Status": "Enabled"}
	return cfn.Resource{
		ID:   "StateBucket",
		Type: "AWS::S3::Bucket",
		Metadata: cfn.Metadata{
			Description: "Pulumi state for every " + scopeOf(tier) + " Ocel has deployed here, one versioned object per stack, read to plan a deploy and to tear one down, and the client CAs the load balancer trusts. Emptying it strands what is already deployed.",
		},
		Properties: properties,
	}
}

func stateBucketOutput(tier environment.Tier) cfn.Output {
	return cfn.Output{
		Name:        outputStateBucket,
		Description: "S3 bucket storing the Pulumi state Ocel plans every " + string(tier) + " deploy and teardown from, one versioned object per " + scopeOf(tier) + " stack, and the records the load balancer's trust store is applied from.",
		Value:       cfn.Ref("StateBucket"),
	}
}

func artifactBucketResource(name string) cfn.Resource {
	return cfn.Resource{
		ID:   "ArtifactBucket",
		Type: "AWS::S3::Bucket",
		Metadata: cfn.Metadata{
			Description: "Staging area for the Lambda code and payloads Ocel uploads before a stack references them, and the bytecode a function compiles on first boot. A deployed function keeps its own copy, and these objects age out on a lifecycle rule.",
		},
		Properties: bucketProperties(name, map[string]any{
			"Id":                             "expire-artifacts",
			"Status":                         "Enabled",
			"ExpirationInDays":               artifactExpirationDays,
			"AbortIncompleteMultipartUpload": map[string]any{"DaysAfterInitiation": artifactAbortMultipartDays},
		}),
	}
}

func artifactBucketOutput() cfn.Output {
	return cfn.Output{
		Name:        outputArtifactBucket,
		Description: "S3 bucket Ocel stages function code, payloads and compiled bytecode in. Objects age out on a lifecycle rule.",
		Value:       cfn.Ref("ArtifactBucket"),
	}
}

func edgeBucketResource(name string) cfn.Resource {
	return cfn.Resource{
		ID:   "EdgeBucket",
		Type: "AWS::S3::Bucket",
		Metadata: cfn.Metadata{
			Description: "Everything an edge may serve or read: each release's static assets, page entries, route tables, edge bundles and image-optimizer config, keyed by kind and release. Removed with the release that wrote it.",
		},
		Properties: bucketProperties(name, map[string]any{
			"Id":                             "abort-incomplete-uploads",
			"Status":                         "Enabled",
			"AbortIncompleteMultipartUpload": map[string]any{"DaysAfterInitiation": artifactAbortMultipartDays},
		}),
	}
}

func edgeBucketPolicyResource() cfn.Resource {
	return cfn.Resource{
		ID:   "EdgeBucketPolicy",
		Type: "AWS::S3::BucketPolicy",
		Metadata: cfn.Metadata{
			Description: "The only bucket policy Ocel writes: CloudFront distributions in this account, and no one else, read the static assets in the edge bucket over an origin access control. One statement for the whole account, so no deploy rewrites it.",
		},
		Properties: map[string]any{
			"Bucket": cfn.Ref("EdgeBucket"),
			"PolicyDocument": iam.Document([]iam.Statement{{
				Sid:            "AllowCloudFrontOriginAccessControlRead",
				Principal:      iam.Principal{Service: iam.CloudFrontPrincipal},
				Actions:        []string{"s3:GetObject"},
				ResourceValues: []any{cfn.Sub("${EdgeBucket.Arn}/" + string(naming.ObjectAssets) + "/*")},
				Condition: map[string]any{
					"StringEquals": map[string]any{"AWS:SourceAccount": cfn.Ref("AWS::AccountId")},
				},
			}}),
		},
	}
}

func edgeBucketOutputs() []cfn.Output {
	return []cfn.Output{
		{
			Name:        outputEdgeBucket,
			Description: "S3 bucket storing everything an edge may serve or read: static assets, page entries, route tables, edge bundles and image-optimizer config.",
			Value:       cfn.Ref("EdgeBucket"),
		},
		{
			Name:        outputEdgeBucketARN,
			Description: "ARN of that bucket, handed to every part stack that grants a read inside it.",
			Value:       cfn.GetAtt("EdgeBucket", "Arn"),
		},
	}
}

func cacheBucketResource(name string) cfn.Resource {
	return cfn.Resource{
		ID:   "CacheBucket",
		Type: "AWS::S3::Bucket",
		Metadata: cfn.Metadata{
			Description: "What only an app's own code reads: each release's fetch cache, use-cache entries and tag clock. A fetch-cache or use-cache entry no release rewrote within the lifecycle rules' days ages out; the tag clock never does.",
		},
		Properties: bucketProperties(name, cacheLifecycle()...),
	}
}

func cacheLifecycle() []any {
	rules := []any{map[string]any{
		"Id":                             "abort-incomplete-uploads",
		"Status":                         "Enabled",
		"AbortIncompleteMultipartUpload": map[string]any{"DaysAfterInitiation": artifactAbortMultipartDays},
	}}
	for _, kind := range naming.ExpiringObjectKinds {
		rules = append(rules, map[string]any{
			"Id":               "expire-" + string(kind),
			"Status":           "Enabled",
			"Prefix":           string(kind) + naming.PathSeparator,
			"ExpirationInDays": naming.CacheObjectLifetimeDays,
		})
	}
	return rules
}

func cacheBucketOutput() cfn.Output {
	return cfn.Output{
		Name:        outputCacheBucket,
		Description: "S3 bucket storing what only an app's own code reads: the fetch cache, use-cache entries and tag clock.",
		Value:       cfn.Ref("CacheBucket"),
	}
}
