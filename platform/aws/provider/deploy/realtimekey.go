package deploy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

const (
	outputKeyRealtimeHost = "realtimeHost"
	outputKeyAPIARN       = "apiArn"
	outputKeyNamespace    = "namespace"
	outputKeySigningKey   = "signingKeySecret"

	realtimeSocketPath = "/event/realtime"
)

type SigningKeysAPI interface {
	GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
	CreateSecret(ctx context.Context, in *secretsmanager.CreateSecretInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error)
	DeleteSecret(ctx context.Context, in *secretsmanager.DeleteSecretInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.DeleteSecretOutput, error)
	ListSecrets(ctx context.Context, in *secretsmanager.ListSecretsInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.ListSecretsOutput, error)
}

func signingKeyPath(root, project, env string) string {
	return strings.Join([]string{root, project, env}, "/")
}

func signingKeySecret(root, project, env, logicalName string) string {
	return signingKeyPath(root, project, env) + "/" + logicalName
}

func readSigningKey(ctx context.Context, keys SigningKeysAPI, name string) ([]byte, bool, error) {
	if keys == nil {
		return nil, false, errors.New("no Secrets Manager client configured")
	}
	out, err := keys.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(name)})
	if err != nil {
		var notFound *smtypes.ResourceNotFoundException
		if errors.As(err, &notFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read %s: %w", name, err)
	}
	seed, err := base64.StdEncoding.DecodeString(aws.ToString(out.SecretString))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, false, fmt.Errorf("secret %s holds no Ed25519 signing key; delete it and re-deploy to mint a new one", name)
	}
	return seed, true, nil
}

func mintSigningKey() ([]byte, error) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	return seed, nil
}

func ensureSigningKey(ctx context.Context, keys SigningKeysAPI, name string) ([]byte, error) {
	seed, found, err := readSigningKey(ctx, keys, name)
	if err != nil || found {
		return seed, err
	}
	minted, err := mintSigningKey()
	if err != nil {
		return nil, fmt.Errorf("generate the signing key for %s: %w", name, err)
	}
	_, err = keys.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name:         aws.String(name),
		Description:  aws.String("Ocel: the Ed25519 key an app signs its realtime tokens with."),
		SecretString: aws.String(base64.StdEncoding.EncodeToString(minted)),
		Tags:         []smtypes.Tag{{Key: aws.String("ocel:managed-by"), Value: aws.String("ocel")}},
	})
	var exists *smtypes.ResourceExistsException
	switch {
	case err == nil:
		return minted, nil
	case !errors.As(err, &exists):
		return nil, fmt.Errorf("write %s: %w", name, err)
	}
	seed, found, err = readSigningKey(ctx, keys, name)
	if err != nil {
		return nil, fmt.Errorf("read %s a concurrent deploy created: %w", name, err)
	}
	if !found {
		return nil, fmt.Errorf("%s was created by a concurrent deploy and is gone again", name)
	}
	return seed, nil
}

func previewSigningKey(ctx context.Context, keys SigningKeysAPI, name string) ([]byte, error) {
	seed, found, err := readSigningKey(ctx, keys, name)
	if err != nil || found {
		return seed, err
	}
	return mintSigningKey()
}

func listSigningKeys(ctx context.Context, keys SigningKeysAPI, path string) ([]string, error) {
	if keys == nil {
		return nil, errors.New("no Secrets Manager client configured")
	}
	prefix := path + "/"
	var names []string
	pages := secretsmanager.NewListSecretsPaginator(keys, &secretsmanager.ListSecretsInput{
		Filters: []smtypes.Filter{{Key: smtypes.FilterNameStringTypeName, Values: []string{prefix}}},
	})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", prefix, err)
		}
		for _, secret := range page.SecretList {
			if name := aws.ToString(secret.Name); strings.HasPrefix(name, prefix) {
				names = append(names, name)
			}
		}
	}
	return names, nil
}

func deleteSigningKeys(ctx context.Context, keys SigningKeysAPI, names []string) error {
	for _, name := range names {
		_, err := keys.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{SecretId: aws.String(name), ForceDeleteWithoutRecovery: aws.Bool(true)})
		var notFound *smtypes.ResourceNotFoundException
		if err != nil && !errors.As(err, &notFound) {
			return fmt.Errorf("delete %s: %w", name, err)
		}
	}
	return nil
}

func collectRealtimeBinding(ctx context.Context, keys SigningKeysAPI, name string, fields map[string]any) (*bindingsv1.Binding, error) {
	values := map[string]string{}
	for _, key := range []string{outputKeyHost, outputKeyRealtimeHost, outputKeyAPIARN, outputKeyNamespace, outputKeySigningKey} {
		value, err := requireStringField(fields, name, key)
		if err != nil {
			return nil, err
		}
		values[key] = value
	}
	secret := values[outputKeySigningKey]
	seed, found, err := readSigningKey(ctx, keys, secret)
	if err != nil {
		return nil, fmt.Errorf("read the signing key for %s: %w", name, err)
	}
	if !found {
		return nil, fmt.Errorf("realtime %s signs its tokens with the key in %s, and that secret is gone; re-deploy to mint a new one", name, secret)
	}
	return &bindingsv1.Binding{
		Name: name,
		Properties: &bindingsv1.Binding_Realtime{Realtime: &bindingsv1.RealtimeProperties{
			Transport:  bindingsv1.RealtimeTransport_REALTIME_TRANSPORT_APPSYNC_EVENTS,
			Url:        "wss://" + values[outputKeyRealtimeHost] + realtimeSocketPath,
			Host:       values[outputKeyHost],
			SigningKey: seed,
			VerifyKey:  ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey),
		}},
		Grants: realtimeGrants(values[outputKeyAPIARN], values[outputKeyNamespace]),
	}, nil
}

func realtimeGrants(apiARN, namespace string) []*bindingsv1.Grant {
	return []*bindingsv1.Grant{
		{Label: "connect", Actions: []string{"appsync:EventConnect"}, Resources: []string{apiARN}},
		{Label: "publish", Actions: []string{"appsync:EventPublish"}, Resources: []string{apiARN + "/channelNamespace/" + namespace}},
	}
}
