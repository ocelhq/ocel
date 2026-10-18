package run

import (
	"context"
	"sync"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/google"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

var runOn = v1.Platform{OS: "linux", Architecture: "amd64"}

type Images struct {
	Pull   func(ctx context.Context, ref string) (v1.Image, error)
	pulled sync.Map
}

func (i *Images) Based(ctx context.Context, ref string) (v1.Image, error) {
	cached, _ := i.pulled.LoadOrStore(ref, &gcloud.Memo[v1.Image]{})
	return cached.(*gcloud.Memo[v1.Image]).Get(func() (v1.Image, error) { return i.Pull(context.WithoutCancel(ctx), ref) })
}

func PullBase(ctx context.Context, ref string) (v1.Image, error) {
	pinned, err := name.NewDigest(ref)
	if err != nil {
		return nil, err
	}
	return remote.Image(pinned,
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(google.Keychain),
		remote.WithPlatform(runOn))
}
