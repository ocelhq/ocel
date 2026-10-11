package gcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"google.golang.org/api/artifactregistry/v1"

	"github.com/ocelhq/ocel/pkg/environment"
)

const (
	genericArtifacts = "GENERIC"
	pluginPackage    = "previewgate"
	pluginFile       = "previewgate.wasm"
	pluginVersionLen = 12
	pluginRepository = "-gate"
)

type pluginImages struct{ p *Provider }

func (i pluginImages) Ensure(ctx context.Context, plugin []byte) (string, error) {
	c, err := i.p.openClients(ctx)
	if err != nil {
		return "", err
	}
	service, err := c.Repositories()
	if err != nil {
		return "", err
	}
	repository := c.Repository(environment.TierPreview) + pluginRepository
	path := repositoryPath(c, repository)
	if err := ensurePluginRepository(ctx, c, service, repository, path); err != nil {
		return "", err
	}
	digest := sha256.Sum256(plugin)
	version := hex.EncodeToString(digest[:])[:pluginVersionLen]
	_, err = attempted(ctx, service.Projects.Locations.Repositories.Packages.Versions.Get(path+"/packages/"+pluginPackage+"/versions/"+version).Context(ctx).Do)
	if err != nil && !absent(err) {
		return "", fmt.Errorf("look for version %s of the preview gate plugin in %s: %w", version, repository, err)
	}
	if err != nil {
		if _, err := attempted(ctx, service.Projects.Locations.Repositories.GenericArtifacts.Upload(path, &artifactregistry.UploadGenericArtifactRequest{
			Filename:  pluginFile,
			PackageId: pluginPackage,
			VersionId: version,
		}).Media(bytes.NewReader(plugin)).Context(ctx).Do); err != nil {
			return "", fmt.Errorf("upload the preview gate plugin to %s: %w", repository, err)
		}
	}
	return path + "/genericArtifacts/" + pluginPackage + ":" + version, nil
}

func (i pluginImages) Delete(ctx context.Context) error {
	c, err := i.p.openClients(ctx)
	if err != nil {
		return err
	}
	service, err := c.Repositories()
	if err != nil {
		return err
	}
	repository := c.Repository(environment.TierPreview) + pluginRepository
	deleting, err := attempted(ctx, service.Projects.Locations.Repositories.Delete(repositoryPath(c, repository)).Context(ctx).Do)
	if absent(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete the %s repository: %w", repository, err)
	}
	return bootstrap{clients: c}.repositoryAwaited(ctx, "deleting the "+repository+" repository", deleting)
}

func ensurePluginRepository(ctx context.Context, c *clients, service *artifactregistry.Service, name, path string) error {
	_, err := attempted(ctx, service.Projects.Locations.Repositories.Get(path).Context(ctx).Do)
	if err == nil {
		return nil
	}
	if !absent(err) {
		return fmt.Errorf("look for the %s repository: %w", name, err)
	}
	creating, err := attempted(ctx, service.Projects.Locations.Repositories.Create(repositoryParent(c), &artifactregistry.Repository{
		Format:      genericArtifacts,
		Description: "the plugin that runs the preview gate on the load balancer",
	}).RepositoryId(name).Context(ctx).Do)
	if taken(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create the %s repository: %w", name, err)
	}
	return bootstrap{clients: c}.repositoryAwaited(ctx, "creating the "+name+" repository", creating)
}
