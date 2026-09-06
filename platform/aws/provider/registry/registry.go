package registry

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/ocelhq/ocel/pkg/providerkit"
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

func Resolve(ctx context.Context, api ECRAPI) (providerkit.RegistryTarget, error) {
	out, err := api.GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	if err != nil {
		return providerkit.RegistryTarget{}, fmt.Errorf("mint a login for this account's image registry: %w", err)
	}
	if len(out.AuthorizationData) == 0 {
		return providerkit.RegistryTarget{}, errors.New("ECR answered a login request with no authorization data, so there is nothing to push an image with")
	}
	granted := out.AuthorizationData[0]
	decoded, err := base64.StdEncoding.DecodeString(aws.ToString(granted.AuthorizationToken))
	if err != nil {
		return providerkit.RegistryTarget{}, fmt.Errorf("decode the registry login ECR minted: %w", err)
	}
	username, password, found := strings.Cut(string(decoded), ":")
	if !found || username == "" || password == "" {
		return providerkit.RegistryTarget{}, errors.New("the registry login ECR minted is not a user:password pair, so nothing can log in with it")
	}
	server := aws.ToString(granted.ProxyEndpoint)
	if _, rest, split := strings.Cut(server, "://"); split {
		server = rest
	}
	server = strings.TrimSuffix(server, "/")
	if server == "" {
		return providerkit.RegistryTarget{}, errors.New("ECR minted a login that names no registry endpoint, so there is nowhere to push to")
	}
	return providerkit.RegistryTarget{
		Server:    server,
		Namespace: Namespace,
		Username:  username,
		Password:  password,
	}, nil
}

func Owns(target providerkit.RegistryTarget) bool {
	return target.Username == ecrUsername && target.Namespace == Namespace
}

type images struct {
	api    ECRAPI
	target providerkit.RegistryTarget
	pushed providerkit.ImageStore
}

func Images(target providerkit.RegistryTarget, api ECRAPI) providerkit.ImageStore {
	return images{api: api, target: target, pushed: providerkit.RegistryImages(target)}
}

func (i images) String() string { return "images pushed to this account's ECR at " + i.target.Server }

func (i images) GoString() string { return i.String() }

func (i images) ImageDestination() string { return i.target.Server }

func (i images) Has(ctx context.Context, push providerkit.ImagePush) (bool, error) {
	return i.pushed.Has(ctx, push)
}

func (i images) Push(ctx context.Context, push providerkit.ImagePush, report providerkit.Reporter) error {
	repository, err := repositoryOf(i.target, push.Target)
	if err != nil {
		return err
	}
	if err := ensure(ctx, i.api, repository); err != nil {
		return err
	}
	return i.pushed.Push(ctx, push, report)
}

func repositoryOf(target providerkit.RegistryTarget, coordinate string) (string, error) {
	rest, found := strings.CutPrefix(coordinate, target.Server+"/")
	if !found {
		return "", fmt.Errorf("%s is not a coordinate under %s, so there is no repository of this account's to hold it", coordinate, target.Server)
	}
	repository, _, _ := strings.Cut(rest, "@")
	if at := strings.LastIndex(repository, ":"); at > strings.LastIndex(repository, "/") {
		repository = repository[:at]
	}
	if repository == "" {
		return "", fmt.Errorf("%s names no repository", coordinate)
	}
	return repository, nil
}

func ensure(ctx context.Context, api ECRAPI, name string) error {
	_, err := api.CreateRepository(ctx, &ecr.CreateRepositoryInput{
		RepositoryName:     aws.String(name),
		ImageTagMutability: ecrtypes.ImageTagMutabilityImmutable,
		Tags:               []ecrtypes.Tag{{Key: aws.String(managedByTag), Value: aws.String(managedByOcel)}},
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
