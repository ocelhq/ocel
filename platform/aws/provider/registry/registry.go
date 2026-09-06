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

const managedByTag = "ocel:managed-by"

type ECRAPI interface {
	CreateRepository(ctx context.Context, in *ecr.CreateRepositoryInput, opts ...func(*ecr.Options)) (*ecr.CreateRepositoryOutput, error)
	GetAuthorizationToken(ctx context.Context, in *ecr.GetAuthorizationTokenInput, opts ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error)
}

func Resolve(ctx context.Context, api ECRAPI, repositories []string) (providerkit.RegistryTarget, error) {
	for _, repository := range repositories {
		if err := ensure(ctx, api, Namespace+"/"+repository); err != nil {
			return providerkit.RegistryTarget{}, err
		}
	}
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

func ensure(ctx context.Context, api ECRAPI, name string) error {
	_, err := api.CreateRepository(ctx, &ecr.CreateRepositoryInput{
		RepositoryName:     aws.String(name),
		ImageTagMutability: ecrtypes.ImageTagMutabilityImmutable,
		Tags:               []ecrtypes.Tag{{Key: aws.String(managedByTag), Value: aws.String("ocel")}},
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
