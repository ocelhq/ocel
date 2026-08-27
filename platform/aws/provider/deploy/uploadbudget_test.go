package deploy

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type blockingUploader struct {
	mu     sync.Mutex
	live   int
	peak   int
	arrive chan struct{}
	hold   chan struct{}
}

func (b *blockingUploader) HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	return nil, &s3types.NotFound{}
}

func (b *blockingUploader) PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	b.mu.Lock()
	b.live++
	if b.live > b.peak {
		b.peak = b.live
	}
	b.mu.Unlock()

	b.arrive <- struct{}{}
	<-b.hold

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
	uploader := &blockingUploader{
		arrive: make(chan struct{}, len(apps)*uploadConcurrency*4),
		hold:   make(chan struct{}),
	}
	cfg := Config{
		ArtifactRoot:       staticAssetApps(t, apps, uploadConcurrency),
		Env:                "prod",
		Slug:               "shop",
		AssetBucket:        "assets",
		Uploader:           uploader,
		CacheStoreBucket:   "isr",
		CacheStoreUploader: uploader,
	}

	var group sync.WaitGroup
	failures := make([]error, len(apps))
	for slot, app := range apps {
		group.Add(1)
		go func() {
			defer group.Done()
			coord := storageCoordinate("prod", "shop", app, fixedRelease(t))
			failures[slot] = pushStaticAssetSet(context.Background(), cfg, app, frameworkNext, coord)
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
	close(uploader.hold)
	group.Wait()

	for slot, err := range failures {
		if err != nil {
			t.Fatalf("pushStaticAssetSet(%s) = %v", apps[slot], err)
		}
	}
	if uploader.peak > uploadConcurrency {
		t.Errorf("%d uploads were in flight at once, want at most %d: the apps standing up side by side share one budget", uploader.peak, uploadConcurrency)
	}
}
