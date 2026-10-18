package signin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/rds/auth"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/ocelhq/ocel/pkg/provider"
)

const TokenRenewal = 10 * time.Minute

type SecretsAPI interface {
	GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

type Sources struct {
	Secrets   SecretsAPI
	MintToken func(ctx context.Context, endpoint, username string) (string, error)
}

func SourcesFor(cfg aws.Config) Sources {
	return Sources{
		Secrets: secretsmanager.NewFromConfig(cfg),
		MintToken: func(ctx context.Context, endpoint, username string) (string, error) {
			return auth.BuildAuthToken(ctx, endpoint, cfg.Region, username, cfg.Credentials)
		},
	}
}

type Password struct {
	Value  string
	Minted bool
}

func (s Sources) Read(ctx context.Context, binding provider.Binding) (Password, bool, error) {
	if binding.HasIAMAuthentication() {
		if s.MintToken == nil {
			return Password{}, false, errors.New("nothing here mints an IAM authentication token")
		}
		endpoint := net.JoinHostPort(binding.Properties[provider.PropertyHost], binding.Properties[provider.PropertyPort])
		token, err := s.MintToken(ctx, endpoint, binding.Properties[provider.PropertyUsername])
		if err != nil {
			return Password{}, false, fmt.Errorf("mint a token to sign in to %s with: %w", binding.Name, err)
		}
		return Password{Value: token, Minted: true}, true, nil
	}
	secretARN := binding.PasswordSecret()
	if secretARN == "" {
		return Password{}, false, nil
	}
	password, err := readSecret(ctx, s.Secrets, secretARN)
	if err != nil {
		return Password{}, false, fmt.Errorf("read the password of %s from %s: %w", binding.Name, secretARN, err)
	}
	return Password{Value: password}, true, nil
}

func readSecret(ctx context.Context, secrets SecretsAPI, secretARN string) (string, error) {
	out, err := secrets.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &secretARN})
	if err != nil {
		return "", err
	}
	if out.SecretString == nil {
		return "", errors.New("the secret has no string value")
	}
	var parsed struct {
		Password string `json:"password"`
	}
	if err := json.Unmarshal([]byte(*out.SecretString), &parsed); err != nil || parsed.Password == "" {
		return "", errors.New("the secret holds no password")
	}
	return parsed.Password, nil
}
