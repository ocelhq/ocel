package live

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"google.golang.org/protobuf/encoding/protojson"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

type SecretsAPI interface {
	GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

func currentPasswords(ctx context.Context, secrets SecretsAPI, bindings []live.Binding, records []variablestore.StoredBinding, values map[string]string) error {
	for i, record := range records {
		l := bindings[i]
		if l.Type != bindingsv1.BindingType_BINDING_TYPE_POSTGRES {
			continue
		}
		published := &variablestorev1.PublishedBinding{}
		if err := protojson.Unmarshal(record.Value, published); err != nil || published.GetBinding().GetPostgres() == nil {
			continue
		}
		secretARN := provider.PublishedBindingOf(published).PasswordSecret()
		if secretARN == "" {
			continue
		}
		password, err := readPassword(ctx, secrets, secretARN)
		if err != nil {
			return fmt.Errorf("read the current password of %s: %w", l.Name, err)
		}
		binding := published.GetBinding()
		binding.GetPostgres().Password = password
		encoded, err := protojson.Marshal(binding)
		if err != nil {
			return fmt.Errorf("render %s with its current password: %w", l.Name, err)
		}
		values[l.Key] = string(encoded)
	}
	return nil
}

func readPassword(ctx context.Context, secrets SecretsAPI, secretARN string) (string, error) {
	out, err := secrets.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &secretARN})
	if err != nil {
		return "", err
	}
	if out.SecretString == nil {
		return "", fmt.Errorf("secret %s has no string value", secretARN)
	}
	var parsed struct {
		Password string `json:"password"`
	}
	if err := json.Unmarshal([]byte(*out.SecretString), &parsed); err != nil || parsed.Password == "" {
		return "", fmt.Errorf("secret %s holds no password", secretARN)
	}
	return parsed.Password, nil
}
