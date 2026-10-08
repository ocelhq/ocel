package registry

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/ocelhq/ocel/pkg/provider"
)

var longAgo = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

var now = longAgo.Add(30 * 24 * time.Hour)

const (
	shopWeb    = "ocel/shop.web"
	registryAt = "123456789012.dkr.ecr.us-east-1.amazonaws.com"
)

func ref(tag string) string { return registryAt + "/" + shopWeb + ":" + tag }

func anECRTarget() provider.RegistryTarget {
	return provider.RegistryTarget{Server: registryAt, Namespace: Namespace, Username: "AWS", Password: "tok3n"}
}

func (f *fakeECR) DescribeImages(_ context.Context, in *ecr.DescribeImagesInput, _ ...func(*ecr.Options)) (*ecr.DescribeImagesOutput, error) {
	if f.describeErr != nil {
		return nil, f.describeErr
	}
	repository := aws.ToString(in.RepositoryName)
	tags, found := f.tagged[repository]
	if !found {
		return nil, &ecrtypes.RepositoryNotFoundException{Message: aws.String(repository + " is gone")}
	}
	f.described++
	start := 0
	if in.NextToken != nil {
		start, _ = strconv.Atoi(*in.NextToken)
	}
	size := f.pageSize
	if size == 0 {
		size = len(tags)
	}
	end := min(start+size, len(tags))
	out := &ecr.DescribeImagesOutput{}
	for _, tag := range tags[start:end] {
		out.ImageDetails = append(out.ImageDetails, ecrtypes.ImageDetail{
			ImageTags:     []string{tag},
			ImagePushedAt: aws.Time(f.pushedAt[tag]),
		})
	}
	if end < len(tags) {
		out.NextToken = aws.String(strconv.Itoa(end))
	}
	return out, nil
}

func pushedLongAgo(tags ...string) map[string]time.Time {
	pushed := map[string]time.Time{}
	for _, tag := range tags {
		pushed[tag] = longAgo
	}
	return pushed
}

func TestASweepDeletesEveryImageOfTheRepositoryNothingKeeps(t *testing.T) {
	t.Parallel()

	api := &fakeECR{tagged: map[string][]string{shopWeb: {"sha256-old", "sha256-kept", "sha256-older"}}, pushedAt: pushedLongAgo("sha256-old", "sha256-kept", "sha256-older")}

	removed, err := sweepRepository(context.Background(), api, anECRTarget(), shopWeb, map[string]bool{ref("sha256-kept"): true}, recordedAs(map[string]bool{ref("sha256-kept"): true}), now)
	if err != nil {
		t.Fatalf("sweepRepository() = %v", err)
	}

	if want := []string{ref("sha256-old"), ref("sha256-older")}; !slices.Equal(removed, want) {
		t.Errorf("sweepRepository() removed %v, want %v", removed, want)
	}
	if got := api.tagged[shopWeb]; !slices.Equal(got, []string{"sha256-kept"}) {
		t.Errorf("the repository holds %v, want only the image a kept release pins", got)
	}
}

func TestASweepLeavesAnImagePushedMoreRecentlyThanThePushedBefore(t *testing.T) {
	t.Parallel()

	api := &fakeECR{
		tagged:   map[string][]string{shopWeb: {"sha256-old", "sha256-just-pushed"}},
		pushedAt: map[string]time.Time{"sha256-old": longAgo, "sha256-just-pushed": now.Add(-5 * time.Minute)},
	}

	removed, err := sweepRepository(context.Background(), api, anECRTarget(), shopWeb, nil, recordedAs(nil), now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("sweepRepository() = %v", err)
	}

	if want := []string{ref("sha256-old")}; !slices.Equal(removed, want) {
		t.Errorf("sweepRepository() removed %v, want %v: another deploy pushes an image before it records the release that runs it, and deleting that image fails the deploy", removed, want)
	}
}

func TestASweepReadsEveryPageAndDeletesInBatchesECRAccepts(t *testing.T) {
	t.Parallel()

	var tags []string
	for i := range 250 {
		tags = append(tags, fmt.Sprintf("sha256-%03d", i))
	}
	api := &fakeECR{tagged: map[string][]string{shopWeb: slices.Clone(tags)}, pushedAt: pushedLongAgo(tags...), pageSize: 100}

	removed, err := sweepRepository(context.Background(), api, anECRTarget(), shopWeb, nil, recordedAs(nil), now)
	if err != nil {
		t.Fatalf("sweepRepository() = %v", err)
	}

	if len(removed) != 250 || len(api.tagged[shopWeb]) != 0 {
		t.Errorf("sweepRepository() removed %d images and left %v, want all 250 of a repository that spans three pages", len(removed), api.tagged[shopWeb])
	}
	if api.deleteCalls != 3 {
		t.Errorf("sweepRepository() called BatchDeleteImage %d times, want 3: it takes 100 image ids a call", api.deleteCalls)
	}
}

func TestASweepOfARepositoryThatIsGoneIsDone(t *testing.T) {
	t.Parallel()

	removed, err := sweepRepository(context.Background(), &fakeECR{}, anECRTarget(), shopWeb, nil, recordedAs(nil), now)
	if err != nil || len(removed) != 0 {
		t.Errorf("sweepRepository() = %v, %v, want nothing removed and no error: the repository a first deploy has not created yet holds nothing to reclaim", removed, err)
	}
}

func TestASweepECRRefusesSaysWhichRepository(t *testing.T) {
	t.Parallel()

	api := &fakeECR{tagged: map[string][]string{shopWeb: {"sha256-old"}}, describeErr: errors.New("AccessDeniedException: not authorized to perform ecr:DescribeImages")}

	_, err := sweepRepository(context.Background(), api, anECRTarget(), shopWeb, nil, recordedAs(nil), now)
	if err == nil {
		t.Fatal("sweepRepository() = nil for a listing ECR refused")
	}
}

func TestRemovingImagesDeletesTheOnesNoRecordedReleaseKeeps(t *testing.T) {
	t.Parallel()

	api := &fakeECR{tagged: map[string][]string{shopWeb: {"sha256-one", "sha256-two", "sha256-three"}}}

	removed, err := removeImages(context.Background(), api, anECRTarget(), []string{ref("sha256-one"), ref("sha256-two")}, map[string]bool{ref("sha256-two"): true}, recordedAs(map[string]bool{ref("sha256-two"): true}))
	if err != nil {
		t.Fatalf("removeImages() = %v", err)
	}

	if want := []string{ref("sha256-one")}; !slices.Equal(removed, want) {
		t.Errorf("removeImages() removed %v, want %v", removed, want)
	}
	if got := api.tagged[shopWeb]; !slices.Equal(got, []string{"sha256-two", "sha256-three"}) {
		t.Errorf("the repository holds %v, want the image another release keeps and the one nobody named", got)
	}
}

func aLoggedInECR() *fakeECR {
	return &fakeECR{token: "AWS:tok3n", endpoint: "https://" + registryAt}
}

func TestReconcilingKeepsTheImageTheReleaseRunsAndEveryOneARecordedReleaseRuns(t *testing.T) {
	t.Parallel()

	api := aLoggedInECR()
	api.tagged = map[string][]string{shopWeb: {"sha256-new", "sha256-kept", "sha256-leaked"}}
	api.pushedAt = pushedLongAgo("sha256-new", "sha256-kept", "sha256-leaked")

	removed, err := Reconcile(context.Background(), api, ref("sha256-new"), recordedAs(map[string]bool{ref("sha256-kept"): true}), now)
	if err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if want := []string{ref("sha256-leaked")}; !slices.Equal(removed, want) {
		t.Errorf("Reconcile() removed %v, want %v: the release's own image is not yet in any record, and the kept release's is", removed, want)
	}
}

func TestReconcilingAnImageOfAnotherRegistryTouchesNothing(t *testing.T) {
	t.Parallel()

	api := aLoggedInECR()
	api.tagged = map[string][]string{shopWeb: {"sha256-old"}}
	api.pushedAt = pushedLongAgo("sha256-old")

	removed, err := Reconcile(context.Background(), api, "ghcr.io/acme/shop.web:sha256-new", recordedAs(nil), now)
	if err != nil || len(removed) != 0 || api.deleteCalls != 0 {
		t.Errorf("Reconcile() = %v, %v with %d deletes, want nothing: the project's own registry is not this account's to prune", removed, err, api.deleteCalls)
	}
}

func TestForgettingDeletesTheImagesAStackRanThatNoRecordedReleaseRuns(t *testing.T) {
	t.Parallel()

	api := aLoggedInECR()
	api.tagged = map[string][]string{shopWeb: {"sha256-one", "sha256-shared", "sha256-other"}}

	removed, err := Forget(context.Background(), api, anECRTarget(),
		[]string{ref("sha256-one"), ref("sha256-shared"), "ghcr.io/acme/shop.web:sha256-elsewhere"},
		recordedAs(map[string]bool{ref("sha256-shared"): true}), now)
	if err != nil {
		t.Fatalf("Forget() = %v", err)
	}

	if want := []string{ref("sha256-one")}; !slices.Equal(removed, want) {
		t.Errorf("Forget() removed %v, want %v: an image another stack still runs stays, and one in another registry is not this account's", removed, want)
	}
	if got := api.tagged[shopWeb]; !slices.Equal(got, []string{"sha256-shared", "sha256-other"}) {
		t.Errorf("the repository holds %v, want the image a stack still runs and the one nobody named", got)
	}
}

func TestForgettingTheLastImagesOfARepositoryDeletesTheRepository(t *testing.T) {
	t.Parallel()

	api := aLoggedInECR()
	api.tagged = map[string][]string{shopWeb: {"sha256-one", "sha256-leaked"}}
	api.pushedAt = pushedLongAgo("sha256-one", "sha256-leaked")

	removed, err := Forget(context.Background(), api, anECRTarget(), []string{ref("sha256-one")}, recordedAs(nil), now)
	if err != nil {
		t.Fatalf("Forget() = %v", err)
	}

	if want := []string{ref("sha256-one"), ref("sha256-leaked")}; !slices.Equal(removed, want) {
		t.Errorf("Forget() removed %v, want %v: once no stack of the project runs an image from the repository, an image no record names is one nothing will run", removed, want)
	}
	if want := []string{shopWeb}; !slices.Equal(api.deletedRepositories, want) {
		t.Errorf("Forget() deleted the repositories %v, want %v: an empty repository left after the project's last stack is a husk every destroyed project adds", api.deletedRepositories, want)
	}
}

func TestForgettingKeepsARepositoryAnotherStackStillRunsAnImageFrom(t *testing.T) {
	t.Parallel()

	api := aLoggedInECR()
	api.tagged = map[string][]string{shopWeb: {"sha256-one", "sha256-kept", "sha256-leaked"}}
	api.pushedAt = pushedLongAgo("sha256-one", "sha256-kept", "sha256-leaked")

	removed, err := Forget(context.Background(), api, anECRTarget(), []string{ref("sha256-one")}, recordedAs(map[string]bool{ref("sha256-kept"): true}), now)
	if err != nil {
		t.Fatalf("Forget() = %v", err)
	}

	if want := []string{ref("sha256-one")}; !slices.Equal(removed, want) {
		t.Errorf("Forget() removed %v, want %v: the repository's next reconcile reclaims the rest", removed, want)
	}
	if len(api.deletedRepositories) != 0 {
		t.Errorf("Forget() deleted %v, a repository a preview still pulls from", api.deletedRepositories)
	}
}

func TestForgettingStillDeletesOneRepositoryWhenAnotherRefusesItsDeletes(t *testing.T) {
	t.Parallel()

	const shopAPI = "ocel/shop.api"
	api := aLoggedInECR()
	api.tagged = map[string][]string{shopWeb: {"sha256-one"}, shopAPI: {"sha256-two"}}
	api.pushedAt = pushedLongAgo("sha256-one", "sha256-two")
	api.refusedIn = shopAPI

	_, err := Forget(context.Background(), api, anECRTarget(), []string{ref("sha256-one"), registryAt + "/" + shopAPI + ":sha256-two"}, recordedAs(nil), now)
	if err == nil {
		t.Fatal("Forget() = nil for a repository whose deletes ECR refused")
	}

	if want := []string{shopWeb}; !slices.Equal(api.deletedRepositories, want) {
		t.Errorf("Forget() deleted the repositories %v, want %v: one repository refusing its deletes must not leave every other one behind", api.deletedRepositories, want)
	}
}

func TestForgettingLeavesARepositoryHoldingAnImageAnotherDeployJustPushed(t *testing.T) {
	t.Parallel()

	api := aLoggedInECR()
	api.tagged = map[string][]string{shopWeb: {"sha256-one", "sha256-just-pushed"}}
	api.pushedAt = map[string]time.Time{"sha256-one": longAgo, "sha256-just-pushed": now.Add(5 * time.Minute)}

	if _, err := Forget(context.Background(), api, anECRTarget(), []string{ref("sha256-one")}, recordedAs(nil), now); err != nil {
		t.Fatalf("Forget() = %v, want nothing: ECR refusing to delete a repository that still holds an image is the repository staying", err)
	}

	if got := api.tagged[shopWeb]; !slices.Equal(got, []string{"sha256-just-pushed"}) {
		t.Errorf("the repository holds %v, want the image a deploy pushed before it recorded the release that runs it", got)
	}
	if len(api.deletedRepositories) != 0 {
		t.Errorf("Forget() deleted %v while it held an image", api.deletedRepositories)
	}
}

func recordedAs(kept map[string]bool) Recorded {
	return func(context.Context) (map[string]bool, error) { return kept, nil }
}

func TestForgettingRemovesAnImageItNamesHoweverRecentlyItWasPushed(t *testing.T) {
	t.Parallel()

	api := aLoggedInECR()
	api.tagged = map[string][]string{shopWeb: {"sha256-one", "sha256-old"}}
	api.pushedAt = map[string]time.Time{"sha256-one": now.Add(-10 * time.Second), "sha256-old": longAgo}

	removed, err := Forget(context.Background(), api, anECRTarget(), []string{ref("sha256-one"), ref("sha256-old"), ref("sha256-gone")}, recordedAs(nil), now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("Forget() = %v", err)
	}

	if want := []string{ref("sha256-one"), ref("sha256-old")}; !slices.Equal(removed, want) {
		t.Errorf("Forget() removed %v, want %v: a deploy that failed seconds after pushing reclaims the image only its own record names", removed, want)
	}
}

func TestARemovalLeavesAnImageARecordNamesByTheTimeItDeletes(t *testing.T) {
	t.Parallel()

	api := aLoggedInECR()
	api.tagged = map[string][]string{shopWeb: {"sha256-one", "sha256-old"}}
	api.pushedAt = pushedLongAgo("sha256-one", "sha256-old")
	reads := 0
	recorded := func(context.Context) (map[string]bool, error) {
		reads++
		if reads == 1 {
			return nil, nil
		}
		return map[string]bool{ref("sha256-one"): true}, nil
	}

	removed, err := Forget(context.Background(), api, anECRTarget(), []string{ref("sha256-one"), ref("sha256-old")}, recorded, now)
	if err != nil {
		t.Fatalf("Forget() = %v", err)
	}

	if want := []string{ref("sha256-old")}; !slices.Equal(removed, want) {
		t.Errorf("Forget() removed %v, want %v: a deploy that recorded sha256-one after the first read found it pushed and will pull it", removed, want)
	}
	if got := api.tagged[shopWeb]; !slices.Equal(got, []string{"sha256-one"}) {
		t.Errorf("the repository holds %v, want the image a deploy recorded since the first read", got)
	}
}

func TestAReconcileLeavesAnImageARecordNamesByTheTimeItDeletes(t *testing.T) {
	t.Parallel()

	api := aLoggedInECR()
	api.tagged = map[string][]string{shopWeb: {"sha256-new", "sha256-recorded-since"}}
	api.pushedAt = pushedLongAgo("sha256-new", "sha256-recorded-since")
	reads := 0
	recorded := func(context.Context) (map[string]bool, error) {
		reads++
		if reads == 1 {
			return nil, nil
		}
		return map[string]bool{ref("sha256-recorded-since"): true}, nil
	}

	removed, err := Reconcile(context.Background(), api, ref("sha256-new"), recorded, now)
	if err != nil || len(removed) != 0 {
		t.Errorf("Reconcile() = %v, %v, want nothing removed: the image a record names by the time of the delete stays", removed, err)
	}
}
