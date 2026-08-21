package bootstrap

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
)

func TestRunStamps(t *testing.T) {
	t.Run("every stack it writes carries the schema, its own digest and the writer", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		standInCloudflare(t, &fakeEdge{kind: "cloudflare"})

		req := everything()
		req.Writer = "1.9.0"
		if err := Run(context.Background(), apisOf(cfn, ssmc, iamc, preloadedStore()), req, nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		for _, name := range cfn.stacks() {
			stamp := cfn.stampOf(name)
			if stamp.Schema != RequiredSchema {
				t.Errorf("%s carries schema %d, want %d", name, stamp.Schema, RequiredSchema)
			}
			if want := TemplateDigest(cfn.template(name)); stamp.Digest != want {
				t.Errorf("%s carries digest %q, want the sha256 of its own body %q", name, stamp.Digest, want)
			}
			if stamp.WrittenBy != "1.9.0" {
				t.Errorf("%s was written by %q, want 1.9.0", name, stamp.WrittenBy)
			}
		}
		deployed, err := CheckDeployed(context.Background(), cfn)
		if err != nil {
			t.Fatalf("CheckDeployed: %v", err)
		}
		if stale := deployed.Stale(featureNames()); len(stale) != 0 {
			t.Errorf("a bootstrap this build just wrote reads as stale: %+v", stale)
		}
	})

	t.Run("a dev build stamps a version that never parses", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		standInCloudflare(t, &fakeEdge{})

		req := Request{Writer: writerFor("dev", "cafebabe")}
		if err := Run(context.Background(), apisOf(cfn, ssmc, iamc, preloadedStore()), req, nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		stamp := cfn.stampOf(StackName)
		if stamp.WrittenBy != "dev+cafebabe" {
			t.Errorf("written by %q, want dev+cafebabe", stamp.WrittenBy)
		}
		if Writer(stamp.WrittenBy).Release() {
			t.Errorf("%q must never read as a release", stamp.WrittenBy)
		}
	})

	t.Run("an update keeps the auto-heal the account opted into", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		standInCloudflare(t, &fakeEdge{})

		apis := apisOf(cfn, ssmc, iamc, preloadedStore())
		if err := Run(context.Background(), apis, Request{Writer: "1.0.0"}, nil, nil); err != nil {
			t.Fatalf("first Run: %v", err)
		}
		cfn.tags[StackName] = append(cfn.tags[StackName], cfntypes.Tag{
			Key: aws.String(TagAutoHeal), Value: aws.String("true"),
		})
		if err := Run(context.Background(), apis, Request{Writer: "1.1.0"}, nil, nil); err != nil {
			t.Fatalf("second Run: %v", err)
		}
		stamp := cfn.stampOf(StackName)
		if stamp.WrittenBy != "1.1.0" {
			t.Errorf("written by %q, want the second run's version", stamp.WrittenBy)
		}
		if !stamp.AutoHeal {
			t.Error("an update wiped the auto-heal the account had opted into")
		}
	})
}
