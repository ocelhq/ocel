package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/buildproxy"
	"github.com/ocelhq/ocel/pkg/runtime/bindingproxy"
	"github.com/ocelhq/ocel/pkg/valkey"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
	"github.com/ocelhq/ocel/platform/aws/provider/bucket"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
)

func (p *Provider) ForwardPorts(ctx context.Context, req provider.PortForwardRequest, _ progress.Log) ([]provider.PortForward, error) {
	if len(req.Bindings) == 0 {
		return nil, nil
	}
	deployed, err := p.bootstrapped(ctx, req.Tier)
	if err != nil {
		return nil, err
	}
	if err := p.requireBootstrapped(deployed, req.Tier); err != nil {
		return nil, err
	}
	resolved, err := readForwardedPasswords(ctx, secretsmanager.NewFromConfig(p.aws), req.Bindings)
	if err != nil {
		return nil, err
	}
	clients := bastion.NewClients(p.aws)
	reconciled, err := bastion.Reconcile(ctx, clients, bastion.Spec{
		Tier:     req.Tier,
		Boundary: deployed.AppBoundaryARN,
		Ports:    []int{deploy.PostgresPort, valkey.Port},
	})
	if err != nil {
		return nil, err
	}
	forwards, err := reconciled.Forward(ctx, clients, bastion.OpenSession(p.aws), req.ReportFailure, req.Bindings)
	if err != nil {
		return nil, err
	}
	for at := range forwards {
		forwards[at].Properties = resolved[forwards[at].Binding]
	}
	return forwards, nil
}

type secretValues interface {
	GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

func readForwardedPasswords(ctx context.Context, secrets secretValues, bindings []provider.Binding) (map[string]map[string]string, error) {
	resolved := map[string]map[string]string{}
	for _, binding := range bindings {
		secretARN := binding.PasswordSecret()
		if secretARN == "" || binding.Properties[provider.PropertyPassword] != "" {
			continue
		}
		password, err := readSecretPassword(ctx, secrets, secretARN)
		if err != nil {
			return nil, fmt.Errorf("read the password of %s from %s: %w", binding.Name, secretARN, err)
		}
		resolved[binding.Name] = map[string]string{provider.PropertyPassword: password}
	}
	return resolved, nil
}

func readSecretPassword(ctx context.Context, secrets secretValues, secretARN string) (string, error) {
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

func (p *Provider) ServeBindingProxy(ctx context.Context, req provider.BindingProxyRequest, _ progress.Log) (provider.BindingProxy, error) {
	return buildproxy.Serve(ctx, req, []provider.BindingType{provider.BindingBucket}, func(ctx context.Context, grants []provider.BindingGrant) (provider.BindingProxy, error) {
		deployed, err := p.bootstrapped(ctx, req.Tier)
		if err != nil {
			return provider.BindingProxy{}, err
		}
		if err := p.requireBootstrapped(deployed, req.Tier); err != nil {
			return provider.BindingProxy{}, err
		}
		objects := s3.NewFromConfig(p.aws)
		table := dynamodb.NewFromConfig(p.aws)
		presigner := s3.NewPresignClient(objects)
		served := make([]bindingproxy.Grant, 0, len(grants))
		for _, grant := range grants {
			buckets := buildproxy.ListBucketNames(grant)
			served = append(served, bindingproxy.Grant{Grantee: grant.Grantee, Services: bindingproxy.Services{Buckets: bucket.New(bucket.Config{
				DDB:              table,
				Presigner:        presigner,
				Objects:          objects,
				Table:            deployed.StateTable,
				SessionKeyPrefix: naming.SessionKeyPrefix(req.Slug, req.Env),
				Granted:          func() []string { return slices.Clone(buckets) },
			})}})
		}
		proxy, err := bindingproxy.Serve(served, req.ReportFailure)
		if err != nil {
			return provider.BindingProxy{}, err
		}
		sessions := make([]provider.BindingSession, 0, len(proxy.Sessions))
		for _, session := range proxy.Sessions {
			sessions = append(sessions, provider.BindingSession{Grantee: session.Grantee, SessionToken: session.Token})
		}
		return provider.BindingProxy{Address: proxy.Address, Sessions: sessions, Close: func() { _ = proxy.Close() }}, nil
	})
}
