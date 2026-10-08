package images

import (
	"context"
	"fmt"
	"net/http"

	"github.com/ocelhq/ocel/pkg/provider"

	"github.com/containerd/errdefs"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/daemon"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type daemonStore struct{}

func DaemonStore() provider.ImageStore { return daemonStore{} }

func (daemonStore) String() string { return "images written to the local docker daemon" }

func (d daemonStore) GoString() string { return d.String() }

func (daemonStore) Destination() string { return "the local docker daemon" }

func (daemonStore) ProbePush(context.Context, string) error { return nil }

func (daemonStore) Has(ctx context.Context, push provider.ImagePush) (bool, error) {
	ref, err := name.NewTag(push.ImageRef, name.Insecure)
	if err != nil {
		return false, fmt.Errorf("%q names nowhere the daemon can store an image: %w", push.ImageRef, err)
	}
	_, err = daemon.Image(ref, daemon.WithContext(ctx))
	if errdefs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("ask the local docker daemon for %s: %w", push.ImageRef, err)
	}
	return true, nil
}

func (daemonStore) Remove(ctx context.Context, imageRef string) error {
	host, err := DockerHostFromEnv()
	if err != nil {
		return err
	}
	transport := host.Transport()
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, "http://docker/images/"+imageRef, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("remove %s from the daemon at %s: %w", imageRef, host.Address, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNotFound:
		return nil
	}
	return fmt.Errorf("the daemon at %s answered %q removing %s: %s", host.Address, resp.Status, imageRef, readErrorBody(resp.Body))
}

func (daemonStore) Push(ctx context.Context, push provider.ImagePush, _ progress.Log) error {
	if push.Built == nil {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s's image is written straight into the local docker daemon, and this release has no image it was built into", push.App)
	}
	ref, err := name.NewTag(push.ImageRef, name.Insecure)
	if err != nil {
		return fmt.Errorf("%q names nowhere the daemon can store an image: %w", push.ImageRef, err)
	}
	if _, err := daemon.Write(ref, push.Built, daemon.WithContext(ctx)); err != nil {
		return fmt.Errorf("write %s's image into the local docker daemon: %w", push.App, err)
	}
	return nil
}
