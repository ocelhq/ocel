package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"google.golang.org/api/artifactregistry/v1"
	"google.golang.org/api/googleapi"
	runv1 "google.golang.org/api/run/v1"
	run "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
)

func artifactTag(image string) (string, bool) {
	if strings.Contains(image, "@") {
		return "", false
	}
	host, path, found := strings.Cut(image, "/")
	location, registry := strings.CutSuffix(host, dockerRegistryHost)
	if !found || !registry || location == "" {
		return "", false
	}
	at := strings.LastIndex(path, ":")
	if at < 0 {
		return "", false
	}
	path, tag := path[:at], path[at+1:]
	segments := strings.SplitN(path, "/", 3)
	if len(segments) != 3 || tag == "" {
		return "", false
	}
	return fmt.Sprintf("projects/%s/locations/%s/repositories/%s/packages/%s/tags/%s",
		segments[0], location, segments[1], url.PathEscape(segments[2]), tag), true
}

func (p *Provider) ReconcileImages(ctx context.Context, _ provider.StackRef, app, imageRef string, images provider.ImageStore, progress progress.Log) error {
	if _, tagged := artifactTag(imageRef); !tagged {
		return p.removeUnusedPushedImage(ctx, app, imageRef, images, progress)
	}
	p.untagUnusedImages(ctx, []string{imageRef}, progress)
	return nil
}

func (p *Provider) removeUnusedPushedImage(ctx context.Context, app, imageRef string, images provider.ImageStore, progress progress.Log) error {
	if images == nil || !strings.HasPrefix(imageRef, images.Destination()+"/") {
		return nil
	}
	clients, err := p.openClients(ctx)
	if err != nil {
		return err
	}
	revisions, err := clients.RunV1()
	if err != nil {
		return err
	}
	running, err := isRunByARevision(ctx, revisions, clients.project, imageRef)
	if err != nil {
		return fmt.Errorf("read whether a revision still runs %s: %w", imageRef, err)
	}
	if running {
		return nil
	}
	resources.RemovePushedImages(ctx, images, app, []string{imageRef}, progress)
	return nil
}

func isRunByARevision(ctx context.Context, revisions *runv1.APIService, project, image string) (bool, error) {
	running, err := attempted(ctx, func(call ...googleapi.CallOption) (*runv1.ListRevisionsResponse, error) {
		return revisions.Namespaces.Revisions.List("namespaces/" + project).
			LabelSelector(imageLabel + "=" + imageLabelValue(image)).Limit(1).Context(ctx).Do(call...)
	})
	if err != nil {
		return false, err
	}
	return len(running.Items) > 0, nil
}

func imagesOf(revisions ...*run.GoogleCloudRunV2Revision) []string {
	var images []string
	for _, revision := range revisions {
		for _, container := range revision.Containers {
			if container.Image != "" && !slices.Contains(images, container.Image) {
				images = append(images, container.Image)
			}
		}
	}
	return images
}

func (p *Provider) readRevisionImages(ctx context.Context, services *run.Service, path string) ([]string, error) {
	revision, err := attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleCloudRunV2Revision, error) {
		return services.Projects.Locations.Services.Revisions.Get(path).Context(ctx).Do(call...)
	})
	if err != nil {
		return nil, err
	}
	return imagesOf(revision), nil
}

func (p *Provider) listServiceImages(ctx context.Context, services *run.Service, path string) ([]string, error) {
	var images []string
	page := ""
	for {
		listed, err := attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleCloudRunV2ListRevisionsResponse, error) {
			return services.Projects.Locations.Services.Revisions.List(path).PageToken(page).Context(ctx).Do(call...)
		})
		if err != nil {
			return nil, err
		}
		for _, image := range imagesOf(listed.Revisions...) {
			if !slices.Contains(images, image) {
				images = append(images, image)
			}
		}
		if page = listed.NextPageToken; page == "" {
			return images, nil
		}
	}
}

const imageLabel = "ocel-image"

func imageLabelValue(image string) string {
	sum := sha256.Sum256([]byte(image))
	return hex.EncodeToString(sum[:16])
}

func (p *Provider) untagUnusedImages(ctx context.Context, images []string, progress progress.Log) {
	tagged := map[string]string{}
	for _, image := range images {
		if name, ok := artifactTag(image); ok {
			tagged[name] = image
		}
	}
	if len(tagged) == 0 {
		return
	}
	clients, err := p.openClients(ctx)
	if err != nil {
		ensureProgress(progress).Warn(fmt.Sprintf("Left %s tagged, as what still runs them could not be read: %v", strings.Join(slices.Sorted(maps.Keys(tagged)), ", "), err))
		return
	}
	revisions, err := clients.RunV1()
	if err != nil {
		ensureProgress(progress).Warn(fmt.Sprintf("Left %s tagged, as what still runs them could not be read: %v", strings.Join(slices.Sorted(maps.Keys(tagged)), ", "), err))
		return
	}
	repositories, err := clients.Repositories()
	if err != nil {
		ensureProgress(progress).Warn(fmt.Sprintf("Left %s tagged, as Artifact Registry could not be reached: %v", strings.Join(slices.Sorted(maps.Keys(tagged)), ", "), err))
		return
	}
	for _, name := range slices.Sorted(maps.Keys(tagged)) {
		running, err := isRunByARevision(ctx, revisions, clients.project, tagged[name])
		if err != nil {
			ensureProgress(progress).Warn(fmt.Sprintf("Left %s tagged, as whether a revision still runs it could not be read: %v", name, err))
			continue
		}
		if running {
			continue
		}
		_, err = attempted(ctx, func(call ...googleapi.CallOption) (*struct{}, error) {
			_, err := repositories.Projects.Locations.Repositories.Packages.Tags.Delete(name).Context(ctx).Do(call...)
			return nil, err
		})
		switch {
		case err == nil, absent(err):
		case answeredCode(err) == http.StatusForbidden:
			ensureProgress(progress).Warn(fmt.Sprintf("Left %s tagged, as this credential may not untag it, so the repository keeps the image until it is untagged: "+
				"grant roles/artifactregistry.repoAdmin on the repository, as `ocel permissions deploy` prints", name))
		default:
			ensureProgress(progress).Warn(fmt.Sprintf("Left %s tagged after no revision ran it, so the repository keeps the image until it is untagged: %v", name, err))
		}
	}
}

const digestTagPrefix = "sha256-"

const imageRetaggedLabel = "ocel-image-retagged"

func isImageMissing(err error, image string) bool {
	return err != nil && strings.Contains(err.Error(), "Image '"+image+"' not found")
}

func (p *Provider) ensureImageTag(ctx context.Context, c *clients, image, version string) (string, error) {
	name, tagged := artifactTag(image)
	if !tagged {
		return "", nil
	}
	at := strings.LastIndex(name, "/tags/")
	packageName, tag := name[:at], name[at+len("/tags/"):]
	digest, isDigestTag := strings.CutPrefix(tag, digestTagPrefix)
	if !isDigestTag {
		return "", nil
	}
	repositories, err := c.Repositories()
	if err != nil {
		return "", err
	}
	held, err := attempted(ctx, func(call ...googleapi.CallOption) (*artifactregistry.Tag, error) {
		return repositories.Projects.Locations.Repositories.Packages.Tags.Get(name).Context(ctx).Do(call...)
	})
	if err == nil {
		return held.Version, nil
	}
	if !absent(err) {
		return "", err
	}
	if version == "" {
		if strings.Contains(digest, naming.WordSeparator) {
			return "", fmt.Errorf("%s was untagged by a prune since it was pushed, and its tag names no version to tag again: deploy again to push it", image)
		}
		version = packageName + "/versions/sha256:" + digest
	}
	_, err = attempted(ctx, func(call ...googleapi.CallOption) (*artifactregistry.Tag, error) {
		return repositories.Projects.Locations.Repositories.Packages.Tags.
			Create(packageName, &artifactregistry.Tag{Version: version}).TagId(tag).Context(ctx).Do(call...)
	})
	if err != nil {
		return "", fmt.Errorf("tag %s again, which a prune untagged after this deploy found it pushed: %w", image, err)
	}
	return version, nil
}
