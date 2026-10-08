package registry

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/ocelhq/ocel/pkg/provider"
)

func (f *fakeECR) BatchDeleteImage(_ context.Context, in *ecr.BatchDeleteImageInput, _ ...func(*ecr.Options)) (*ecr.BatchDeleteImageOutput, error) {
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	repository := aws.ToString(in.RepositoryName)
	var out ecr.BatchDeleteImageOutput
	for _, id := range in.ImageIds {
		tag := aws.ToString(id.ImageTag)
		if !slices.Contains(f.tagged[repository], tag) {
			out.Failures = append(out.Failures, ecrtypes.ImageFailure{
				ImageId:       &id,
				FailureCode:   ecrtypes.ImageFailureCodeImageNotFound,
				FailureReason: aws.String("Requested image not found"),
			})
			continue
		}
		f.tagged[repository] = slices.DeleteFunc(f.tagged[repository], func(each string) bool { return each == tag })
		out.ImageIds = append(out.ImageIds, id)
	}
	return &out, nil
}

func anECRStore(api *fakeECR) provider.ImageStore {
	target := provider.RegistryTarget{Server: "123456789012.dkr.ecr.us-east-1.amazonaws.com", Namespace: Namespace, Username: "AWS", Password: "tok3n"}
	return Images(target, api)
}

const shopWebRef = "123456789012.dkr.ecr.us-east-1.amazonaws.com/ocel/shop.web:sha256-abc"

func TestRemovingAnImageDeletesItsTagFromTheRepositoryItWasPushedTo(t *testing.T) {
	t.Parallel()

	api := &fakeECR{tagged: map[string][]string{"ocel/shop.web": {"sha256-abc", "sha256-def"}}}

	if err := anECRStore(api).Remove(context.Background(), shopWebRef); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	if got := api.tagged["ocel/shop.web"]; !slices.Equal(got, []string{"sha256-def"}) {
		t.Errorf("the repository holds %v, want only the tag Remove was not asked for", got)
	}
}

func TestRemovingAnImageThatIsAlreadyGoneIsDone(t *testing.T) {
	t.Parallel()

	for name, api := range map[string]*fakeECR{
		"a tag the repository lacks": {tagged: map[string][]string{"ocel/shop.web": {"sha256-def"}}},
		"a repository that is gone":  {deleteErr: &ecrtypes.RepositoryNotFoundException{Message: aws.String("ocel/shop.web is gone")}},
	} {
		if err := anECRStore(api).Remove(context.Background(), shopWebRef); err != nil {
			t.Errorf("Remove() of %s = %v, want nothing", name, err)
		}
	}
}

func TestRemovingAnImageECRRefusesSaysSo(t *testing.T) {
	t.Parallel()

	api := &fakeECR{deleteErr: errors.New("AccessDeniedException: not authorized to perform ecr:BatchDeleteImage")}
	if err := anECRStore(api).Remove(context.Background(), shopWebRef); err == nil {
		t.Error("Remove() = nil for a delete ECR refused, so the image would be reported reclaimed")
	}
}

func TestRemovingAnImageUnderAnotherRegistryIsRefused(t *testing.T) {
	t.Parallel()

	if err := anECRStore(&fakeECR{}).Remove(context.Background(), "ghcr.io/acme/web:sha256-abc"); err == nil {
		t.Error("Remove() accepted a coordinate under another registry, which no repository of this account's holds")
	}
}
