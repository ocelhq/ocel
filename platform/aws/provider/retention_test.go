package aws

import (
	"context"
	"encoding/base64"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/registry"
)

func recordImage(t *testing.T, store keyvalue.Store, tier environment.Tier, project string, name naming.StackName, images ...string) {
	t.Helper()
	recorded := stackrecords.Stack{Kind: provider.StackApp, App: name.App}
	for _, image := range images {
		recorded.Containers = append(recorded.Containers, provider.AppContainer{Name: name.App, Image: image})
	}
	if err := stackrecords.Write(context.Background(), store, tier, project, name, recorded); err != nil {
		t.Fatal(err)
	}
}

func shopStacks(t *testing.T) (keyvalue.Store, provider.StackRef) {
	t.Helper()
	store := fake.NewKeyValues()
	own := naming.AppStack("prod", "web", naming.NewReleaseToken("b2", ""))
	previous := naming.AppStack("prod", "web", naming.NewReleaseToken("b1", ""))
	preview := naming.AppStack("pr-7", "web", naming.NewReleaseToken("b3", ""))
	recordImage(t, store, environment.TierProduction, "shop", own, "ecr/ocel/shop.web:sha256-own")
	recordImage(t, store, environment.TierProduction, "shop", previous, "ecr/ocel/shop.web:sha256-previous")
	recordImage(t, store, environment.TierPreview, "shop", preview, "ecr/ocel/shop.web:sha256-preview")
	recordImage(t, store, environment.TierProduction, "blog", previous, "ecr/ocel/blog.web:sha256-other")
	return store, provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: own}
}

func TestTheImagesAReconcileOrAForgetKeepsAreWhatEveryOtherStackOfTheProjectRecords(t *testing.T) {
	t.Parallel()

	store, own := shopStacks(t)

	kept, err := otherStacksImages(store, own)(context.Background())
	if err != nil {
		t.Fatalf("otherStacksImages() = %v", err)
	}

	want := map[string]bool{"ecr/ocel/shop.web:sha256-previous": true, "ecr/ocel/shop.web:sha256-preview": true}
	if !maps.Equal(kept, want) {
		t.Errorf("otherStacksImages() = %v, want %v: the stack's own image is kept by the reconcile while its release may run it and taken by its forget, as on gcp, and another project's stacks are not listed at all", kept, want)
	}
}

func projectRegistryImage(tag string) string {
	return fake.RegistryServer + "/acme/shop.web:" + tag
}

func TestForgettingAStackRemovesTheImagesItRanFromTheProjectsRegistryThatNoOtherStackRuns(t *testing.T) {
	t.Parallel()

	store := fake.NewKeyValues()
	own := naming.AppStack("prod", "web", naming.NewReleaseToken("b2", ""))
	other := naming.AppStack("pr-7", "web", naming.NewReleaseToken("b1", ""))
	recordImage(t, store, environment.TierProduction, "shop", own, projectRegistryImage("sha256-own"), projectRegistryImage("sha256-shared"))
	recordImage(t, store, environment.TierPreview, "shop", other, projectRegistryImage("sha256-shared"))
	pushed := fake.NewImages()

	err := forgetImages(context.Background(), store, &repositoryECR{}, provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: own}, "web", pushed, progress.Discard())
	if err != nil {
		t.Fatalf("forgetImages() = %v", err)
	}

	if want := []string{projectRegistryImage("sha256-own")}; !slices.Equal(pushed.Removed(), want) {
		t.Errorf("forgetImages() removed %v, want %v: an image the project's registry holds is reclaimed through its store, and one a preview still runs stays", pushed.Removed(), want)
	}
}

func TestAProjectRegistryThatRefusesARemovalDoesNotFailTheDestroy(t *testing.T) {
	t.Parallel()

	store := fake.NewKeyValues()
	own := naming.AppStack("prod", "web", naming.NewReleaseToken("b2", ""))
	recordImage(t, store, environment.TierProduction, "shop", own, projectRegistryImage("sha256-own"))
	refused := fake.NewImages()
	refused.FailRemovals(errors.New("UNSUPPORTED"))
	said := &fake.Log{}

	err := forgetImages(context.Background(), store, &repositoryECR{}, provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: own}, "web", refused, said)

	if err != nil {
		t.Errorf("forgetImages() = %v, want the destroy to finish: a registry that never deletes would otherwise block every retry", err)
	}
	if !strings.Contains(strings.Join(said.Lines(), "\n"), projectRegistryImage("sha256-own")) {
		t.Errorf("forgetImages() said %v, want a warning naming the image left behind", said.Lines())
	}
}

type countedEntries struct {
	keyvalue.Store
	listed int
}

func (c *countedEntries) List(ctx context.Context, in keyvalue.Partition, under ...string) ([]keyvalue.Entry, error) {
	entries, err := c.Store.List(ctx, in, under...)
	c.listed += len(entries)
	return entries, err
}

func TestAReconcileReadsTheStacksOfItsOwnAppAlone(t *testing.T) {
	t.Parallel()

	store, own := shopStacks(t)
	for _, app := range []string{"api", "admin", "docs", "jobs"} {
		recordImage(t, store, environment.TierPreview, "shop", naming.AppStack("pr-7", app, naming.NewReleaseToken("b3", "")), "ecr/ocel/shop."+app+":sha256-x")
	}
	counted := &countedEntries{Store: store}

	if _, err := otherStacksImages(counted, own)(context.Background()); err != nil {
		t.Fatalf("otherStacksImages() = %v", err)
	}

	if counted.listed != 3 {
		t.Errorf("otherStacksImages() read %d entries, want the 3 stacks of web: the read grows with the app whose repository it sweeps, never with the project's other apps", counted.listed)
	}
}

func TestAProjectNothingRecordsKeepsNoImage(t *testing.T) {
	t.Parallel()

	kept, err := otherStacksImages(fake.NewKeyValues(), provider.StackRef{
		Project: "shop", Tier: environment.TierProduction, Name: naming.AppStack("prod", "web", naming.NewReleaseToken("b1", "")),
	})(context.Background())
	if err != nil || len(kept) != 0 {
		t.Errorf("otherStacksImages() = %v, %v, want none", kept, err)
	}
}

func recordReleaseAboutToRun(t *testing.T, store keyvalue.Store, tier environment.Tier, project string, name naming.StackName, image string) {
	t.Helper()
	recorded := stackrecords.Stack{Kind: provider.StackApp, App: name.App, Image: image}
	if err := stackrecords.Write(context.Background(), store, tier, project, name, recorded); err != nil {
		t.Fatal(err)
	}
}

func TestForgettingAStackWhoseReleaseFailedRemovesTheImageItPushed(t *testing.T) {
	t.Parallel()

	store := fake.NewKeyValues()
	failed := naming.AppStack("pr-7", "web", naming.NewReleaseToken("b1", ""))
	recordReleaseAboutToRun(t, store, environment.TierPreview, "shop", failed, projectRegistryImage("sha256-failed"))
	pushed := fake.NewImages()

	err := forgetImages(context.Background(), store, &repositoryECR{}, provider.StackRef{Project: "shop", Tier: environment.TierPreview, Name: failed}, "web", pushed, progress.Discard())
	if err != nil {
		t.Fatalf("forgetImages() = %v", err)
	}

	if want := []string{projectRegistryImage("sha256-failed")}; !slices.Equal(pushed.Removed(), want) {
		t.Errorf("forgetImages() removed %v, want %v: a release that pushed its image and failed before running it leaves no other record of the image", pushed.Removed(), want)
	}
}

func TestAReconcileKeepsTheImageADeployInFlightIsAboutToRun(t *testing.T) {
	t.Parallel()

	store, own := shopStacks(t)
	starting := naming.AppStack("prod", "web", naming.NewReleaseToken("b4", ""))
	recordReleaseAboutToRun(t, store, environment.TierProduction, "shop", starting, "ecr/ocel/shop.web:sha256-starting")

	kept, err := otherStacksImages(store, own)(context.Background())
	if err != nil {
		t.Fatalf("otherStacksImages() = %v", err)
	}

	if !kept["ecr/ocel/shop.web:sha256-starting"] {
		t.Errorf("otherStacksImages() = %v, want the image a stack records before it runs it", kept)
	}
}

type repositoryECR struct {
	registry.ECRAPI
	tags    []string
	deleted []string
}

func (e *repositoryECR) GetAuthorizationToken(context.Context, *ecr.GetAuthorizationTokenInput, ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error) {
	return &ecr.GetAuthorizationTokenOutput{AuthorizationData: []ecrtypes.AuthorizationData{{
		AuthorizationToken: aws.String(base64.StdEncoding.EncodeToString([]byte("AWS:tok3n"))),
		ProxyEndpoint:      aws.String("https://ecr"),
	}}}, nil
}

func (e *repositoryECR) BatchDeleteImage(_ context.Context, in *ecr.BatchDeleteImageInput, _ ...func(*ecr.Options)) (*ecr.BatchDeleteImageOutput, error) {
	out := &ecr.BatchDeleteImageOutput{}
	for _, id := range in.ImageIds {
		e.tags = slices.DeleteFunc(e.tags, func(tag string) bool { return tag == aws.ToString(id.ImageTag) })
		out.ImageIds = append(out.ImageIds, id)
	}
	return out, nil
}

func (e *repositoryECR) DescribeImages(context.Context, *ecr.DescribeImagesInput, ...func(*ecr.Options)) (*ecr.DescribeImagesOutput, error) {
	out := &ecr.DescribeImagesOutput{}
	for _, tag := range e.tags {
		out.ImageDetails = append(out.ImageDetails, ecrtypes.ImageDetail{ImageTags: []string{tag}, ImagePushedAt: aws.Time(time.Now().Add(-time.Hour))})
	}
	return out, nil
}

func (e *repositoryECR) DeleteRepository(_ context.Context, in *ecr.DeleteRepositoryInput, _ ...func(*ecr.Options)) (*ecr.DeleteRepositoryOutput, error) {
	if len(e.tags) > 0 {
		return nil, &ecrtypes.RepositoryNotEmptyException{Message: aws.String("still holds images")}
	}
	e.deleted = append(e.deleted, aws.ToString(in.RepositoryName))
	return &ecr.DeleteRepositoryOutput{}, nil
}

func TestAReclaimHandedTheECRStoreDeletesTheRepositoryItEmpties(t *testing.T) {
	t.Parallel()

	store := fake.NewKeyValues()
	failed := naming.AppStack("pr-7", "web", naming.NewReleaseToken("b1", ""))
	recordReleaseAboutToRun(t, store, environment.TierPreview, "shop", failed, "ecr/ocel/shop.web:sha256-failed")
	api := &repositoryECR{tags: []string{"sha256-failed"}}
	target, err := registry.Resolve(context.Background(), api)
	if err != nil {
		t.Fatal(err)
	}

	err = forgetImages(context.Background(), store, api, provider.StackRef{Project: "shop", Tier: environment.TierPreview, Name: failed}, "web", registry.Images(target, api), progress.Discard())
	if err != nil {
		t.Fatalf("forgetImages() = %v", err)
	}

	if !slices.Equal(api.deleted, []string{"ocel/shop.web"}) {
		t.Errorf("forgetImages() deleted the repositories %v, want ocel/shop.web: a failed deploy reclaims through the ECR store it pushed with, and that reclaim must empty and delete the repository as a destroy does", api.deleted)
	}
}

type wrappedImages struct{ provider.ImageStore }

func TestAReclaimHandedAWrappedECRStoreStillDeletesTheRepositoryItEmpties(t *testing.T) {
	t.Parallel()

	store := fake.NewKeyValues()
	failed := naming.AppStack("pr-7", "web", naming.NewReleaseToken("b1", ""))
	recordReleaseAboutToRun(t, store, environment.TierPreview, "shop", failed, "ecr/ocel/shop.web:sha256-failed")
	api := &repositoryECR{tags: []string{"sha256-failed"}}
	target, err := registry.Resolve(context.Background(), api)
	if err != nil {
		t.Fatal(err)
	}

	err = forgetImages(context.Background(), store, api, provider.StackRef{Project: "shop", Tier: environment.TierPreview, Name: failed}, "web", wrappedImages{registry.Images(target, api)}, progress.Discard())
	if err != nil {
		t.Fatalf("forgetImages() = %v", err)
	}

	if !slices.Equal(api.deleted, []string{"ocel/shop.web"}) {
		t.Errorf("forgetImages() deleted the repositories %v, want ocel/shop.web: an image under this account's ECR is reclaimed as ECR's, whatever store carries it", api.deleted)
	}
}
