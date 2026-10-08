package images

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

func (r registryStore) Remove(ctx context.Context, imageRef string) error {
	options := []name.Option{}
	if registryScheme(r.target.Server) == "http" {
		options = append(options, name.Insecure)
	}
	tag, err := name.NewTag(imageRef, options...)
	if err != nil {
		return fmt.Errorf("%q names no image a registry can remove: %w", imageRef, err)
	}
	if served, err := name.NewRegistry(r.target.Server, options...); err != nil || tag.RegistryStr() != served.RegistryStr() {
		return fmt.Errorf("%s is not under %s, the registry this store holds credentials for", imageRef, r.target.Server)
	}
	if r.target.Server == gitHubContainerRegistry {
		return r.removePackageVersion(ctx, tag)
	}
	called := []remote.Option{
		remote.WithContext(ctx),
		remote.WithRetryStatusCodes(http.StatusRequestTimeout, http.StatusTooManyRequests,
			http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout),
	}
	if r.target.Username != "" || r.target.Password != "" {
		called = append(called, remote.WithAuth(&authn.Basic{Username: r.target.Username, Password: r.target.Password}))
	}
	err = remote.Delete(tag, called...)
	switch {
	case err == nil, isAbsent(err):
		return nil
	case !isTagDeleteRefused(err):
		return fmt.Errorf("remove %s from %s: %w", imageRef, r.target.Server, err)
	}
	described, err := remote.Head(tag, called...)
	if isAbsent(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("look for %s in %s before removing it: %w", imageRef, r.target.Server, err)
	}
	if err := remote.Delete(tag.Digest(described.Digest.String()), called...); err != nil && !isAbsent(err) {
		return fmt.Errorf("remove %s from %s: %w", imageRef, r.target.Server, err)
	}
	return nil
}

func isTagDeleteRefused(err error) bool {
	var refused *transport.Error
	return errors.As(err, &refused) && (refused.StatusCode == http.StatusMethodNotAllowed || refused.StatusCode == http.StatusBadRequest)
}

func isAbsent(err error) bool {
	var refused *transport.Error
	return errors.As(err, &refused) && refused.StatusCode == http.StatusNotFound
}
