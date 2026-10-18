package albpart

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"google.golang.org/api/artifactregistry/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const (
	genericArtifacts = "GENERIC"
	pluginPackage    = "gate"
	pluginFile       = "gate.wasm"
	pluginVersionLen = 12
	PluginRepository = "-gate"
)

type PluginImages struct {
	OpenClients func(context.Context) (*gcloud.Clients, error)
}

func (i PluginImages) Ensure(ctx context.Context, tier environment.Tier, plugin []byte) (string, error) {
	c, err := i.OpenClients(ctx)
	if err != nil {
		return "", err
	}
	service, err := c.Repositories()
	if err != nil {
		return "", err
	}
	repository := c.Repository(tier) + PluginRepository
	path := gcloud.RepositoryPath(c, repository)
	if err := ensurePluginRepository(ctx, c, service, repository, path); err != nil {
		return "", err
	}
	digest := sha256.Sum256(plugin)
	version := hex.EncodeToString(digest[:])[:pluginVersionLen]
	_, err = googleretry.Attempted(ctx, service.Projects.Locations.Repositories.Packages.Versions.Get(path+"/packages/"+pluginPackage+"/versions/"+version).Context(ctx).Do)
	if err != nil && !gcloud.Absent(err) {
		return "", fmt.Errorf("look for version %s of the gate plugin in %s: %w", version, repository, err)
	}
	if err != nil {
		if _, err := googleretry.Attempted(ctx, service.Projects.Locations.Repositories.GenericArtifacts.Upload(path, &artifactregistry.UploadGenericArtifactRequest{
			Filename:  pluginFile,
			PackageId: pluginPackage,
			VersionId: version,
		}).Media(bytes.NewReader(plugin)).Context(ctx).Do); err != nil {
			return "", fmt.Errorf("upload the gate plugin to %s: %w", repository, err)
		}
	}
	return path + "/genericArtifacts/" + pluginPackage + ":" + version, nil
}

func (i PluginImages) Delete(ctx context.Context, tier environment.Tier) error {
	c, err := i.OpenClients(ctx)
	if err != nil {
		return err
	}
	service, err := c.Repositories()
	if err != nil {
		return err
	}
	repository := c.Repository(tier) + PluginRepository
	deleting, err := googleretry.Attempted(ctx, service.Projects.Locations.Repositories.Delete(gcloud.RepositoryPath(c, repository)).Context(ctx).Do)
	if gcloud.Absent(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete the %s repository: %w", repository, err)
	}
	return bootstrap.Engine{Clients: c}.RepositoryAwaited(ctx, "deleting the "+repository+" repository", deleting)
}

func ensurePluginRepository(ctx context.Context, c *gcloud.Clients, service *artifactregistry.Service, name, path string) error {
	_, err := googleretry.Attempted(ctx, service.Projects.Locations.Repositories.Get(path).Context(ctx).Do)
	if err == nil {
		return nil
	}
	if !gcloud.Absent(err) {
		return fmt.Errorf("look for the %s repository: %w", name, err)
	}
	creating, err := googleretry.Attempted(ctx, service.Projects.Locations.Repositories.Create(gcloud.RepositoryParent(c), &artifactregistry.Repository{
		Format:      genericArtifacts,
		Description: "the plugin that runs the gate on the load balancer",
	}).RepositoryId(name).Context(ctx).Do)
	if gcloud.Taken(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create the %s repository: %w", name, err)
	}
	return bootstrap.Engine{Clients: c}.RepositoryAwaited(ctx, "creating the "+name+" repository", creating)
}
