package gcp

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/api/artifactregistry/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const reasonUnpruned = "it exists with cleanup policies this bootstrap did not name, and what prunes the images a deploy pushes would then be rules nothing here wrote"

const (
	dockerImages     = "DOCKER"
	standardImages   = "STANDARD_REPOSITORY"
	deleteImages     = "DELETE"
	untaggedImages   = "UNTAGGED"
	untaggedLifetime = "604800s"

	dropUntaggedPolicy = "drop-untagged"
)

var repositoryKind = kind{
	noun:          "Artifact Registry repository",
	terraform:     tfArtifactRepository,
	emulatorSkips: true,
	properties: func(region string, _ item) map[string]any {
		return map[string]any{"location": region, "format": "DOCKER"}
	},
	survey: func(ctx context.Context, b bootstrap, _ environment.Tier, target item) (presence, error) {
		return b.repositoryPresence(ctx, target.Name)
	},
	make: func(ctx context.Context, b bootstrap, _ survey, target item) error {
		return b.makeRepository(ctx, target.Name)
	},
	mend: func(ctx context.Context, b bootstrap, _ survey, target item) error {
		return b.pruneRepository(ctx, target.Name)
	},
	take: func(ctx context.Context, b bootstrap, _ survey, target item) (time.Duration, error) {
		return 0, b.takeRepository(ctx, target.Name)
	},
}

func (b bootstrap) makeRepository(ctx context.Context, name string) error {
	service, err := b.clients.Repositories()
	if err != nil {
		return err
	}
	creating, err := googleretry.Attempted(ctx, service.Projects.Locations.Repositories.Create(
		gcloud.RepositoryParent(b.clients), imageRepository()).RepositoryId(name).Context(ctx).Do)
	if gcloud.Taken(err) {
		return b.pruneRepository(ctx, name)
	}
	if err != nil {
		return fmt.Errorf("create the %s image repository: %w", name, err)
	}
	return b.repositoryAwaited(ctx, fmt.Sprintf("creating the %s image repository", name), creating)
}

func (b bootstrap) pruneRepository(ctx context.Context, name string) error {
	service, err := b.clients.Repositories()
	if err != nil {
		return err
	}
	if _, err := googleretry.Attempted(ctx, service.Projects.Locations.Repositories.Patch(gcloud.RepositoryPath(b.clients, name),
		&artifactregistry.Repository{CleanupPolicies: pruningUntaggedImages()}).
		UpdateMask("cleanup_policies").Context(ctx).Do); err != nil {
		return fmt.Errorf("set the %s image repository's cleanup policy to the one this bootstrap names: %w", name, err)
	}
	return nil
}

func imageRepository() *artifactregistry.Repository {
	return &artifactregistry.Repository{
		Format:          dockerImages,
		Mode:            standardImages,
		Description:     "the images ocel deploys into this project",
		CleanupPolicies: pruningUntaggedImages(),
	}
}

func pruningUntaggedImages() map[string]artifactregistry.CleanupPolicy {
	return map[string]artifactregistry.CleanupPolicy{
		dropUntaggedPolicy: {
			Id:     dropUntaggedPolicy,
			Action: deleteImages,
			Condition: &artifactregistry.CleanupPolicyCondition{
				TagState:  untaggedImages,
				OlderThan: untaggedLifetime,
			},
		},
	}
}

func (b bootstrap) takeRepository(ctx context.Context, name string) error {
	service, err := b.clients.Repositories()
	if err != nil {
		return err
	}
	deleting, err := googleretry.Attempted(ctx, service.Projects.Locations.Repositories.Delete(gcloud.RepositoryPath(b.clients, name)).Context(ctx).Do)
	if gcloud.Absent(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete the %s image repository: %w", name, err)
	}
	return b.repositoryAwaited(ctx, fmt.Sprintf("deleting the %s image repository", name), deleting)
}

func (b bootstrap) repositoryAwaited(ctx context.Context, doing string, operation *artifactregistry.Operation) error {
	if operation == nil || operation.Done || operation.Name == "" {
		return repositoryFailed(doing, operation)
	}
	service, err := b.clients.Repositories()
	if err != nil {
		return err
	}
	finished, err := gcloud.Until(ctx, doing, func() (*artifactregistry.Operation, error) {
		return googleretry.Attempted(ctx, service.Projects.Locations.Operations.Get(operation.Name).Context(ctx).Do)
	}, func(polled *artifactregistry.Operation) bool { return polled.Done })
	if err != nil {
		return err
	}
	return repositoryFailed(doing, finished)
}

func repositoryFailed(doing string, operation *artifactregistry.Operation) error {
	if operation == nil || operation.Error == nil {
		return nil
	}
	return fmt.Errorf("%s: %s", doing, operation.Error.Message)
}

func (b bootstrap) repositoryPresence(ctx context.Context, name string) (presence, error) {
	service, err := b.clients.Repositories()
	if err != nil {
		return presence{}, err
	}
	repository, err := googleretry.Attempted(ctx, service.Projects.Locations.Repositories.Get(gcloud.RepositoryPath(b.clients, name)).Context(ctx).Do)
	if gcloud.Absent(err) {
		return presence{}, nil
	}
	if err != nil {
		return presence{}, fmt.Errorf("read the %s image repository: %w", name, err)
	}
	return repositoryPresenceOf(name, repository)
}

func repositoryPresenceOf(name string, repository *artifactregistry.Repository) (presence, error) {
	if repository.Format != dockerImages {
		return presence{}, refusal.Refuse(refusal.CodeInvalid,
			"the %s repository already exists and stores %s packages, and Artifact Registry never changes the format of one: "+
				"Cloud Run runs %s images and nothing this bootstrap does would make it store them.\n"+
				"Delete that repository, or bootstrap under a namespace naming another in %s",
			name, repository.Format, dockerImages, provider.NamespaceEnvVar)
	}
	if !pruned(repository.CleanupPolicies) {
		return presence{present: true, mends: reasonUnpruned}, nil
	}
	return presence{present: true}, nil
}

func pruned(policies map[string]artifactregistry.CleanupPolicy) bool {
	if len(policies) != len(pruningUntaggedImages()) {
		return false
	}
	policy, named := policies[dropUntaggedPolicy]
	return named && policy.Action == deleteImages && policy.Condition != nil &&
		policy.Condition.TagState == untaggedImages && policy.Condition.OlderThan == untaggedLifetime
}
