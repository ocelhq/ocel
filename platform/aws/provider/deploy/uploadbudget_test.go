package deploy

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
)

type blockingArtifactStore struct {
	mu      sync.Mutex
	live    int
	peak    int
	arrive  chan struct{}
	release chan struct{}
}

func (b *blockingArtifactStore) HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	return nil, &s3types.NotFound{}
}

func (b *blockingArtifactStore) PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	b.mu.Lock()
	b.live++
	if b.live > b.peak {
		b.peak = b.live
	}
	b.mu.Unlock()

	b.arrive <- struct{}{}
	<-b.release

	b.mu.Lock()
	b.live--
	b.mu.Unlock()
	return &s3.PutObjectOutput{}, nil
}

func staticAssetApps(t *testing.T, apps []string, assets int) string {
	t.Helper()
	files := map[string]string{}
	for _, app := range apps {
		for slot := range assets {
			files["apps/"+app+"/static/"+strconv.Itoa(slot)+".txt"] = app + " asset " + strconv.Itoa(slot)
		}
	}
	return writeTree(t, files)
}

func TestPublishingAssetsSharesOneBudgetAcrossTheAppsUploadingAtOnce(t *testing.T) {
	apps := []string{"web", "admin"}
	uploader := &blockingArtifactStore{
		arrive:  make(chan struct{}, len(apps)*uploadConcurrency*4),
		release: make(chan struct{}),
	}
	cfg := Config{
		ArtifactRoot:      staticAssetApps(t, apps, uploadConcurrency),
		Env:               "prod",
		Slug:              "shop",
		AssetBucket:       "assets",
		Objects:           uploader,
		CacheStoreBucket:  "isr",
		CacheStoreObjects: uploader,
	}

	var group sync.WaitGroup
	failures := make([]error, len(apps))
	for slot, app := range apps {
		group.Add(1)
		go func() {
			defer group.Done()
			coord := storageCoordinate("prod", "shop", app, fixedRelease(t))
			failures[slot] = pushStaticAssetSet(context.Background(), cfg, app, appbuild.FrameworkNext, coord)
		}()
	}

	for range uploadConcurrency {
		<-uploader.arrive
	}
	select {
	case <-uploader.arrive:
		t.Error("an upload started while the budget was already full: each app took a budget of its own")
	case <-time.After(200 * time.Millisecond):
	}
	close(uploader.release)
	group.Wait()

	for slot, err := range failures {
		if err != nil {
			t.Fatalf("pushStaticAssetSet(%s) = %v", apps[slot], err)
		}
	}
	if uploader.peak > uploadConcurrency {
		t.Errorf("%d uploads were in flight at once, want at most %d: the apps deploying side by side share one budget", uploader.peak, uploadConcurrency)
	}
}
