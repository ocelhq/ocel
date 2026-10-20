package account

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestStateBucket(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
	}{
		{"production", coreBody(defaultNamespace, environment.TierProduction, "")},
		{"preview", coreBody(defaultNamespace, environment.TierPreview, "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := parseTemplateStr(t, tc.template)

			bucket, ok := tmpl.Resources["StateBucket"]
			if !ok {
				t.Fatal("template is missing the StateBucket resource")
			}

			rules := bucket.Properties.LifecycleConfiguration.Rules
			if len(rules) != 2 {
				t.Fatalf("StateBucket lifecycle rules = %d, want exactly 2", len(rules))
			}

			byID := map[string]int{}
			for i, rule := range rules {
				byID[rule.Id] = i
				if rule.Status != "Enabled" {
					t.Errorf("rule %q Status = %q, want Enabled", rule.Id, rule.Status)
				}
				if rule.ExpirationInDays != 0 {
					t.Errorf("rule %q ExpirationInDays = %d, want 0: state checkpoints are current versions and must not expire", rule.Id, rule.ExpirationInDays)
				}
			}

			expire, ok := byID["expire-noncurrent-state"]
			if !ok {
				t.Fatalf("lifecycle rules %v are missing expire-noncurrent-state", rules)
			}
			if got := rules[expire].NoncurrentVersionExpiration.NoncurrentDays; got != stateNoncurrentDays {
				t.Errorf("NoncurrentDays = %d, want %d", got, stateNoncurrentDays)
			}
			if got := rules[expire].NoncurrentVersionExpiration.NewerNoncurrentVersions; got != 0 {
				t.Errorf("NewerNoncurrentVersions = %d, want 0: retained versions survive a teardown forever", got)
			}
			if got := rules[expire].AbortIncompleteMultipartUpload.DaysAfterInitiation; got != stateAbortMultipartDays {
				t.Errorf("AbortIncompleteMultipartUpload = %d, want %d", got, stateAbortMultipartDays)
			}

			sweep, ok := byID["expire-state-delete-markers"]
			if !ok {
				t.Fatalf("lifecycle rules %v are missing expire-state-delete-markers", rules)
			}
			if !rules[sweep].ExpiredObjectDeleteMarker {
				t.Error("ExpiredObjectDeleteMarker = false, want true: a delete marker left behind is an object left behind")
			}
		})
	}
}

func TestTheBootstrapKeepsABucketPerDataClassNamedForItsTierAndClass(t *testing.T) {
	for _, tc := range []struct {
		tier environment.Tier
		want map[string]string
	}{
		{environment.TierProduction, map[string]string{
			"StateBucket":    "ocel-prod-state-" + fixtureBucketTag,
			"ArtifactBucket": "ocel-prod-artifacts-" + fixtureBucketTag,
			"EdgeBucket":     "ocel-prod-edge-" + fixtureBucketTag,
			"CacheBucket":    "ocel-prod-cache-" + fixtureBucketTag,
		}},
		{environment.TierPreview, map[string]string{
			"StateBucket":    "ocel-preview-state-" + fixtureBucketTag,
			"ArtifactBucket": "ocel-preview-artifacts-" + fixtureBucketTag,
			"EdgeBucket":     "ocel-preview-edge-" + fixtureBucketTag,
			"CacheBucket":    "ocel-preview-cache-" + fixtureBucketTag,
		}},
	} {
		t.Run(string(tc.tier), func(t *testing.T) {
			tmpl := parseTemplateStr(t, coreBody(defaultNamespace, tc.tier, ""))

			got := map[string]string{}
			for id, resource := range tmpl.Resources {
				if resource.Type == "AWS::S3::Bucket" {
					got[id] = resource.Properties.BucketName
				}
			}
			if !maps.Equal(got, tc.want) {
				t.Errorf("buckets = %v, want %v", got, tc.want)
			}
			for id := range tc.want {
				bucket := tmpl.Resources[id].Properties
				if bucket.AbacStatus != "Enabled" {
					t.Errorf("%s AbacStatus = %q, want Enabled", id, bucket.AbacStatus)
				}
				pab := bucket.PublicAccessBlockConfiguration
				if !pab.BlockPublicAcls || !pab.BlockPublicPolicy || !pab.IgnorePublicAcls || !pab.RestrictPublicBuckets {
					t.Errorf("%s PublicAccessBlockConfiguration = %+v, want all four blocks true", id, pab)
				}
			}
		})
	}
}

func lifecycleOf(t *testing.T, tier environment.Tier, id string) (expires, abortsAfter int) {
	t.Helper()
	bucket, ok := parseTemplateStr(t, coreBody(defaultNamespace, tier, "")).Resources[id]
	if !ok {
		t.Fatalf("the core declares no %s", id)
	}
	rules := bucket.Properties.LifecycleConfiguration.Rules
	if len(rules) != 1 {
		t.Fatalf("%s lifecycle rules = %d, want exactly 1", id, len(rules))
	}
	if rules[0].Status != "Enabled" {
		t.Errorf("%s lifecycle rule Status = %q, want Enabled", id, rules[0].Status)
	}
	return rules[0].ExpirationInDays, rules[0].AbortIncompleteMultipartUpload.DaysAfterInitiation
}

func TestEachBootstrapBucketAgesOutWhatItsDataClassOutlives(t *testing.T) {
	for _, tier := range bothTiers {
		t.Run(string(tier), func(t *testing.T) {
			for _, tc := range []struct {
				id      string
				expires int
				why     string
			}{
				{"ArtifactBucket", 30, "a deployed function keeps its own copy of the code staged for it"},
				{"EdgeBucket", 0, "what an edge serves goes with the release that wrote it, never on a timer"},
			} {
				expires, abortsAfter := lifecycleOf(t, tier, tc.id)
				if expires != tc.expires {
					t.Errorf("%s expires objects after %d days, want %d: %s", tc.id, expires, tc.expires, tc.why)
				}
				if abortsAfter != artifactAbortMultipartDays {
					t.Errorf("%s aborts incomplete uploads after %d days, want %d", tc.id, abortsAfter, artifactAbortMultipartDays)
				}
			}
		})
	}
}

func TestTheCacheBucketExpiresTheFetchAndUseCachesButNeverTheTagClock(t *testing.T) {
	for _, tier := range bothTiers {
		t.Run(string(tier), func(t *testing.T) {
			bucket := parseTemplateStr(t, coreBody(defaultNamespace, tier, "")).Resources["CacheBucket"]
			expiring := map[string]int{}
			aborts := false
			for _, rule := range bucket.Properties.LifecycleConfiguration.Rules {
				if rule.Status != "Enabled" {
					t.Errorf("rule %q Status = %q, want Enabled", rule.Id, rule.Status)
				}
				if rule.ExpirationInDays > 0 {
					expiring[rule.Prefix] = rule.ExpirationInDays
				}
				if rule.Prefix == "" && rule.AbortIncompleteMultipartUpload.DaysAfterInitiation == artifactAbortMultipartDays {
					aborts = true
				}
			}
			want := map[string]int{"fetch-cache/": 30, "use-cache/": 30}
			if !maps.Equal(expiring, want) {
				t.Errorf("CacheBucket expires %v (prefix: days), want %v: the tag clock is rewritten only on revalidation, and an expired clock stops revalidateTag reaching other instances", expiring, want)
			}
			clock := "tag-clock/acme/prod/web/r1a2b3c4d/tag-clock.json"
			for prefix := range expiring {
				if strings.HasPrefix(clock, prefix) {
					t.Errorf("the rule over %q expires the tag clock", prefix)
				}
			}
			if !aborts {
				t.Errorf("CacheBucket aborts no incomplete upload anywhere in the bucket after %d days", artifactAbortMultipartDays)
			}
		})
	}
}

func TestARebootstrapKeepsTheBucketsTheFirstOneNamed(t *testing.T) {
	stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	frontedBy(t, &fakeEdge{})
	bucketNames := func() []string {
		var names []string
		for _, resource := range parseTemplateStr(t, stacks.template(coreStackName)).Resources {
			if resource.Type == "AWS::S3::Bucket" {
				names = append(names, resource.Properties.BucketName)
			}
		}
		slices.Sort(names)
		return names
	}

	if err := Run(context.Background(), apisOf(stacks, ssmc, iamc, preloadedStore()), defaultNamespace, environment.TierProduction, Request{}, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	first := bucketNames()
	if err := Run(context.Background(), apisOf(stacks, ssmc, iamc, preloadedStore()), defaultNamespace, environment.TierProduction, Request{}, nil); err != nil {
		t.Fatalf("Run again: %v", err)
	}

	if len(first) != 4 {
		t.Fatalf("the first bootstrap named buckets %v, want four", first)
	}
	for _, name := range first {
		tag := name[strings.LastIndex(name, "-")+1:]
		if !naming.IsRandomTag(tag) {
			t.Errorf("bucket %s ends in %q, want a random tag minted for it", name, tag)
		}
	}
	if again := bucketNames(); !slices.Equal(again, first) {
		t.Errorf("the second bootstrap names buckets %v, want the first one's %v: a new name replaces the bucket and strands what it holds", again, first)
	}
}

func TestARebootstrapOverBucketsThatShareNoRandomTagIsRefusedRatherThanRenamingThem(t *testing.T) {
	stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	frontedBy(t, &fakeEdge{})
	if err := Run(context.Background(), apisOf(stacks, ssmc, iamc, preloadedStore()), defaultNamespace, environment.TierProduction, Request{}, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	written := stacks.template(coreStackName)
	stacks.mu.Lock()
	stacks.outputs[coreStackName][outputStateBucket] = "ocel-prod-state"
	stacks.mu.Unlock()

	err := Run(context.Background(), apisOf(stacks, ssmc, iamc, preloadedStore()), defaultNamespace, environment.TierProduction, Request{}, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Run = %v, want an invalid refusal: a new tag renames every bucket, and CloudFormation replaces a renamed bucket", err)
	}
	if again := stacks.template(coreStackName); again != written {
		t.Error("the refused bootstrap rewrote the core stack")
	}
}

func TestOnlyTheEdgeBucketAdmitsCloudFrontAndOnlyToItsAssets(t *testing.T) {
	for _, tier := range bothTiers {
		t.Run(string(tier), func(t *testing.T) {
			tmpl := parseTemplateStr(t, coreBody(defaultNamespace, tier, ""))

			var policies []string
			for id, resource := range tmpl.Resources {
				if resource.Type == "AWS::S3::BucketPolicy" {
					policies = append(policies, id)
				}
			}
			if !slices.Equal(policies, []string{"EdgeBucketPolicy"}) {
				t.Fatalf("bucket policies = %v, want only EdgeBucketPolicy: CloudFront's read is account-wide, so it may reach only what an edge serves", policies)
			}
			policy := tmpl.Resources["EdgeBucketPolicy"].Properties
			if got := policy.Bucket; got != "EdgeBucket" {
				t.Errorf("the policy is attached to %q, want the edge bucket", got)
			}
			statements := policy.PolicyDocument.Statement
			if len(statements) != 1 {
				t.Fatalf("the policy has %d statements, want exactly one: it is written once by the bootstrap and never rewritten per deploy", len(statements))
			}
			grant := statements[0]
			if grant.Effect != "Allow" || !slices.Equal(templateStrings(grant.Action), []string{"s3:GetObject"}) {
				t.Errorf("the grant is %s %v, want Allow s3:GetObject", grant.Effect, grant.Action)
			}
			if grant.Principal.Service != "cloudfront.amazonaws.com" {
				t.Errorf("principal = %q, want the CloudFront service principal and nothing wider", grant.Principal.Service)
			}
			if got := grant.Resource; got != "${EdgeBucket.Arn}/assets/*" {
				t.Errorf("resource = %q, want the edge bucket's assets and nothing else: CloudFront serves only a release's static assets out of it", got)
			}
			if got := grant.Condition.StringEquals["AWS:SourceAccount"]; got != "AWS::AccountId" {
				t.Errorf("source account condition = %q, want this account, so no other account's distribution can read the bucket", got)
			}
		})
	}
}
