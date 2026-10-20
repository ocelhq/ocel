package bootstrap

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/api/artifactregistry/v1"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const reasonUnpruned = "it exists with cleanup policies this bootstrap did not name, and what prunes the images a deploy pushes would then be rules nothing here wrote"

const (
	DockerImages     = "DOCKER"
	StandardImages   = "STANDARD_REPOSITORY"
	DeleteImages     = "DELETE"
	UntaggedImages   = "UNTAGGED"
	UntaggedLifetime = "604800s"

	DropUntaggedPolicy = "drop-untagged"
)

var repositoryKind = KindOps{
	noun:          "Artifact Registry repository",
	terraform:     cost.ArtifactRepositoryType,
	emulatorSkips: true,
	properties: func(region string, _ Resource) map[string]any {
		return map[string]any{"location": region, "format": "DOCKER"}
	},
	survey: func(ctx context.Context, b Engine, _ Survey, target Resource) (Presence, error) {
		return b.repositoryPresence(ctx, target.Name)
	},
	Make: func(ctx context.Context, b Engine, _ Survey, target Resource) error {
		return b.makeRepository(ctx, target.Name)
	},
	mend: func(ctx context.Context, b Engine, _ Survey, target Resource) error {
		return b.pruneRepository(ctx, target.Name)
	},
	Take: func(ctx context.Context, b Engine, _ Survey, target Resource) (time.Duration, error) {
		return 0, b.takeRepository(ctx, target.Name)
	},
}

func (b Engine) makeRepository(ctx context.Context, name string) error {
	service, err := b.Clients.Repositories()
	if err != nil {
		return err
	}
	creating, err := googleretry.Attempted(ctx, service.Projects.Locations.Repositories.Create(
		gcloud.RepositoryParent(b.Clients), ImageRepository()).RepositoryId(name).Context(ctx).Do)
	if gcloud.Taken(err) {
		return b.pruneRepository(ctx, name)
	}
	if err != nil {
		return fmt.Errorf("create the %s image repository: %w", name, err)
	}
	return b.RepositoryAwaited(ctx, fmt.Sprintf("creating the %s image repository", name), creating)
}

func (b Engine) pruneRepository(ctx context.Context, name string) error {
	service, err := b.Clients.Repositories()
	if err != nil {
		return err
	}
	if _, err := googleretry.Attempted(ctx, service.Projects.Locations.Repositories.Patch(gcloud.RepositoryPath(b.Clients, name),
		&artifactregistry.Repository{CleanupPolicies: pruningUntaggedImages()}).
		UpdateMask("cleanup_policies").Context(ctx).Do); err != nil {
		return fmt.Errorf("set the %s image repository's cleanup policy to the one this bootstrap names: %w", name, err)
	}
	return nil
}

func ImageRepository() *artifactregistry.Repository {
	return &artifactregistry.Repository{
		Format:          DockerImages,
		Mode:            StandardImages,
		Description:     "the images ocel deploys into this project",
		CleanupPolicies: pruningUntaggedImages(),
	}
}

func pruningUntaggedImages() map[string]artifactregistry.CleanupPolicy {
	return map[string]artifactregistry.CleanupPolicy{
		DropUntaggedPolicy: {
			Id:     DropUntaggedPolicy,
			Action: DeleteImages,
			Condition: &artifactregistry.CleanupPolicyCondition{
				TagState:  UntaggedImages,
				OlderThan: UntaggedLifetime,
			},
		},
	}
}

func (b Engine) takeRepository(ctx context.Context, name string) error {
	service, err := b.Clients.Repositories()
	if err != nil {
		return err
	}
	deleting, err := googleretry.Attempted(ctx, service.Projects.Locations.Repositories.Delete(gcloud.RepositoryPath(b.Clients, name)).Context(ctx).Do)
	if gcloud.Absent(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete the %s image repository: %w", name, err)
	}
	return b.RepositoryAwaited(ctx, fmt.Sprintf("deleting the %s image repository", name), deleting)
}

func (b Engine) RepositoryAwaited(ctx context.Context, doing string, operation *artifactregistry.Operation) error {
	if operation == nil || operation.Done || operation.Name == "" {
		return repositoryFailed(doing, operation)
	}
	service, err := b.Clients.Repositories()
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

func (b Engine) repositoryPresence(ctx context.Context, name string) (Presence, error) {
	service, err := b.Clients.Repositories()
	if err != nil {
		return Presence{}, err
	}
	repository, err := googleretry.Attempted(ctx, service.Projects.Locations.Repositories.Get(gcloud.RepositoryPath(b.Clients, name)).Context(ctx).Do)
	if gcloud.Absent(err) {
		return Presence{}, nil
	}
	if err != nil {
		return Presence{}, fmt.Errorf("read the %s image repository: %w", name, err)
	}
	return RepositoryPresenceOf(name, repository)
}

func RepositoryPresenceOf(name string, repository *artifactregistry.Repository) (Presence, error) {
	if repository.Format != DockerImages {
		return Presence{}, refusal.Refuse(refusal.CodeInvalid,
			"the %s repository already exists and stores %s packages, and Artifact Registry never changes the format of one: "+
				"Cloud Run runs %s images and nothing this bootstrap does would make it store them.\n"+
				"Delete that repository, or bootstrap under a namespace naming another in %s",
			name, repository.Format, DockerImages, provider.NamespaceEnvVar)
	}
	if !pruned(repository.CleanupPolicies) {
		return Presence{Present: true, Mends: reasonUnpruned}, nil
	}
	return Presence{Present: true}, nil
}

func pruned(policies map[string]artifactregistry.CleanupPolicy) bool {
	if len(policies) != len(pruningUntaggedImages()) {
		return false
	}
	policy, named := policies[DropUntaggedPolicy]
	return named && policy.Action == DeleteImages && policy.Condition != nil &&
		policy.Condition.TagState == UntaggedImages && policy.Condition.OlderThan == UntaggedLifetime
}
