package registry

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/ocelhq/ocel/pkg/providerkit/images"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const Namespace = "ocel"

const (
	ecrUsername   = "AWS"
	managedByTag  = "ocel:managed-by"
	managedByOcel = "ocel"
)

type ECRAPI interface {
	CreateRepository(ctx context.Context, in *ecr.CreateRepositoryInput, opts ...func(*ecr.Options)) (*ecr.CreateRepositoryOutput, error)
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

func (i ecrImages) String() string {
	return "images pushed to this account's ECR at " + i.target.Server
}

func (i ecrImages) GoString() string { return i.String() }

func (i ecrImages) Destination() string { return i.target.Server }

func (i ecrImages) Has(ctx context.Context, push provider.ImagePush) (bool, error) {
	return i.pushed.Has(ctx, push)
}

func (i ecrImages) Push(ctx context.Context, push provider.ImagePush, progress edge.Progress) error {
	repository, err := repositoryOf(i.target, push.ImageRef)
	if err != nil {
		return err
	}
	if err := ensure(ctx, i.api, repository); err != nil {
		return err
	}
	return i.pushed.Push(ctx, push, progress)
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

func ensure(ctx context.Context, api ECRAPI, name string) error {
	_, err := api.CreateRepository(ctx, &ecr.CreateRepositoryInput{
		RepositoryName:             aws.String(name),
		ImageTagMutability:         ecrtypes.ImageTagMutabilityImmutable,
		ImageScanningConfiguration: &ecrtypes.ImageScanningConfiguration{ScanOnPush: true},
		Tags:                       []ecrtypes.Tag{{Key: aws.String(managedByTag), Value: aws.String(managedByOcel)}},
	})
	var exists *ecrtypes.RepositoryAlreadyExistsException
	if errors.As(err, &exists) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create the image repository %s: %w", name, err)
	}
	return nil
}
