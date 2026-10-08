package registry

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	Namespace  = "ocel"
	ProjectTag = "ocel:project"
)

const (
	ecrUsername   = "AWS"
	managedByTag  = "ocel:managed-by"
	managedByOcel = "ocel"
)

type ECRAPI interface {
	DescribeImages(ctx context.Context, in *ecr.DescribeImagesInput, opts ...func(*ecr.Options)) (*ecr.DescribeImagesOutput, error)
	BatchDeleteImage(ctx context.Context, in *ecr.BatchDeleteImageInput, opts ...func(*ecr.Options)) (*ecr.BatchDeleteImageOutput, error)
	DeleteRepository(ctx context.Context, in *ecr.DeleteRepositoryInput, opts ...func(*ecr.Options)) (*ecr.DeleteRepositoryOutput, error)
	CreateRepository(ctx context.Context, in *ecr.CreateRepositoryInput, opts ...func(*ecr.Options)) (*ecr.CreateRepositoryOutput, error)
	DescribeRepositories(ctx context.Context, in *ecr.DescribeRepositoriesInput, opts ...func(*ecr.Options)) (*ecr.DescribeRepositoriesOutput, error)
	TagResource(ctx context.Context, in *ecr.TagResourceInput, opts ...func(*ecr.Options)) (*ecr.TagResourceOutput, error)
	ListTagsForResource(ctx context.Context, in *ecr.ListTagsForResourceInput, opts ...func(*ecr.Options)) (*ecr.ListTagsForResourceOutput, error)
	GetAuthorizationToken(ctx context.Context, in *ecr.GetAuthorizationTokenInput, opts ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error)
}

func Resolve(ctx context.Context, api ECRAPI) (provider.RegistryTarget, error) {
	out, err := api.GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	if err != nil {
		return provider.RegistryTarget{}, fmt.Errorf("mint a login for this account's image registry: %w", err)
	}
	if len(out.AuthorizationData) == 0 {
		return provider.RegistryTarget{}, errors.New("ECR answered a login request with no authorization data, so there is nothing to push an image with")
	}
	granted := out.AuthorizationData[0]
	decoded, err := base64.StdEncoding.DecodeString(aws.ToString(granted.AuthorizationToken))
	if err != nil {
		return provider.RegistryTarget{}, fmt.Errorf("decode the registry login ECR minted: %w", err)
	}
	username, password, found := strings.Cut(string(decoded), ":")
	if !found || username == "" || password == "" {
		return provider.RegistryTarget{}, errors.New("the registry login ECR minted is not a user:password pair, so nothing can log in with it")
	}
	server := aws.ToString(granted.ProxyEndpoint)
	if _, rest, split := strings.Cut(server, "://"); split {
		server = rest
	}
	server = strings.TrimSuffix(server, "/")
	if server == "" {
		return provider.RegistryTarget{}, errors.New("ECR minted a login that names no registry endpoint, so there is nowhere to push to")
	}
	return provider.RegistryTarget{
		Server:    server,
		Namespace: Namespace,
		Username:  username,
		Password:  password,
	}, nil
}

func Owns(target provider.RegistryTarget) bool {
	return target.Username == ecrUsername && target.Namespace == Namespace
}

type ecrImages struct {
	api    ECRAPI
	target provider.RegistryTarget
	pushed provider.ImageStore
}

func Images(target provider.RegistryTarget, api ECRAPI) provider.ImageStore {
	return ecrImages{api: api, target: target, pushed: images.RegistryStore(target)}
}

func HoldsImagesOf(store provider.ImageStore) bool {
	_, ecr := store.(ecrImages)
	return ecr
}

func (i ecrImages) String() string {
	return "images pushed to this account's ECR at " + i.target.Server
}

func (i ecrImages) GoString() string { return i.String() }

func (i ecrImages) Destination() string { return i.target.Server }

func (ecrImages) ProbePush(context.Context, string) error { return nil }

func (i ecrImages) Has(ctx context.Context, push provider.ImagePush) (bool, error) {
	return i.pushed.Has(ctx, push)
}

func (i ecrImages) Push(ctx context.Context, push provider.ImagePush, progress progress.Log) error {
	repository, err := repositoryOf(i.target, push.ImageRef)
	if err != nil {
		return err
	}
	if _, err := ensure(ctx, i.api, repository); err != nil {
		return err
	}
	pushErr := i.pushed.Push(ctx, push, progress)
	if pushErr == nil {
		return nil
	}
	created, err := ensure(ctx, i.api, repository)
	if err != nil || !created && !missingRepository(pushErr) {
		return pushErr
	}
	return i.pushed.Push(ctx, push, progress)
}

func missingRepository(err error) bool {
	said := strings.ReplaceAll(strings.ToLower(err.Error()), "_", " ")
	return strings.Contains(said, "name unknown")
}

func (i ecrImages) Remove(ctx context.Context, imageRef string) error {
	_, err := removeImages(ctx, i.api, i.target, []string{imageRef}, nil, noneRecorded)
	return err
}

func tagOf(imageRef string) (string, error) {
	at := strings.LastIndex(imageRef, ":")
	if at < strings.LastIndex(imageRef, "/") || at+1 == len(imageRef) {
		return "", fmt.Errorf("%s names no tag to remove", imageRef)
	}
	return imageRef[at+1:], nil
}

func repositoryOf(target provider.RegistryTarget, imageRef string) (string, error) {
	rest, found := strings.CutPrefix(imageRef, target.Server+"/")
	if !found {
		return "", fmt.Errorf("%s is not an image ref under %s, so there is no repository of this account's to store it", imageRef, target.Server)
	}
	repository, _, _ := strings.Cut(rest, "@")
	if at := strings.LastIndex(repository, ":"); at > strings.LastIndex(repository, "/") {
		repository = repository[:at]
	}
	if repository == "" {
		return "", fmt.Errorf("%s names no repository", imageRef)
	}
	return repository, nil
}

func ensure(ctx context.Context, api ECRAPI, name string) (bool, error) {
	project, ok := images.RegistryRepositoryProject(strings.TrimPrefix(name, Namespace+"/"))
	if !ok {
		return false, fmt.Errorf("the image repository %s names no project, so no credential limited to a project could ever delete its images", name)
	}
	tags := []ecrtypes.Tag{
		{Key: aws.String(managedByTag), Value: aws.String(managedByOcel)},
		{Key: aws.String(ProjectTag), Value: aws.String(project)},
	}
	_, err := api.CreateRepository(ctx, &ecr.CreateRepositoryInput{
		RepositoryName:             aws.String(name),
		ImageTagMutability:         ecrtypes.ImageTagMutabilityImmutable,
		ImageScanningConfiguration: &ecrtypes.ImageScanningConfiguration{ScanOnPush: true},
		Tags:                       tags,
	})
	var exists *ecrtypes.RepositoryAlreadyExistsException
	if errors.As(err, &exists) {
		return false, tagRepository(ctx, api, name, tags)
	}
	if err != nil {
		return false, fmt.Errorf("create the image repository %s: %w", name, err)
	}
	return true, nil
}

func tagRepository(ctx context.Context, api ECRAPI, name string, tags []ecrtypes.Tag) error {
	described, err := api.DescribeRepositories(ctx, &ecr.DescribeRepositoriesInput{RepositoryNames: []string{name}})
	if err != nil {
		return fmt.Errorf("read the image repository %s to tag it with its project: %w", name, err)
	}
	for _, repository := range described.Repositories {
		listed, err := api.ListTagsForResource(ctx, &ecr.ListTagsForResourceInput{ResourceArn: repository.RepositoryArn})
		if err != nil {
			return fmt.Errorf("read the tags of the image repository %s: %w", name, err)
		}
		if carriesTags(listed.Tags, tags) {
			continue
		}
		if _, err := api.TagResource(ctx, &ecr.TagResourceInput{ResourceArn: repository.RepositoryArn, Tags: tags}); err != nil {
			return fmt.Errorf("tag the image repository %s with its project, without which no credential limited to a project may delete its images: %w", name, err)
		}
	}
	return nil
}

func carriesTags(carried, wanted []ecrtypes.Tag) bool {
	for _, want := range wanted {
		if !slices.ContainsFunc(carried, func(tag ecrtypes.Tag) bool {
			return aws.ToString(tag.Key) == aws.ToString(want.Key) && aws.ToString(tag.Value) == aws.ToString(want.Value)
		}) {
			return false
		}
	}
	return true
}
