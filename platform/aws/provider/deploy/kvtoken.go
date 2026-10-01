package deploy

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/ocelhq/ocel/pkg/kvstore"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

const (
	kvTokenLen      = 64
	kvTokenAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
)

type ParametersAPI interface {
	GetParameter(ctx context.Context, in *ssm.GetParameterInput, optFns ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
	PutParameter(ctx context.Context, in *ssm.PutParameterInput, optFns ...func(*ssm.Options)) (*ssm.PutParameterOutput, error)
	DeleteParameter(ctx context.Context, in *ssm.DeleteParameterInput, optFns ...func(*ssm.Options)) (*ssm.DeleteParameterOutput, error)
}

func kvTokenParameter(root, project, env, logicalName string) string {
	return strings.Join([]string{root, project, env, logicalName}, "/")
}

func mintKVToken() (string, error) {
	var token strings.Builder
	bound := big.NewInt(int64(len(kvTokenAlphabet)))
	for range kvTokenLen {
		n, err := rand.Int(rand.Reader, bound)
		if err != nil {
			return "", err
		}
		token.WriteByte(kvTokenAlphabet[n.Int64()])
	}
	return token.String(), nil
}

func readKVToken(ctx context.Context, params ParametersAPI, name string) (string, bool, error) {
	if params == nil {
		return "", false, errors.New("no SSM client configured")
	}
	out, err := params.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(name), WithDecryption: aws.Bool(true)})
	if err != nil {
		var notFound *ssmtypes.ParameterNotFound
		if errors.As(err, &notFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read %s: %w", name, err)
	}
	return aws.ToString(out.Parameter.Value), true, nil
}

func ensureKVToken(ctx context.Context, params ParametersAPI, name string) (string, error) {
	token, found, err := readKVToken(ctx, params, name)
	if err != nil || found {
		return token, err
	}
	minted, err := mintKVToken()
	if err != nil {
		return "", fmt.Errorf("generate the token for %s: %w", name, err)
	}
	_, err = params.PutParameter(ctx, &ssm.PutParameterInput{
		Name:        aws.String(name),
		Description: aws.String("Ocel: the AUTH token a kv store demands of every client."),
		Value:       aws.String(minted),
		Type:        ssmtypes.ParameterTypeSecureString,
		Overwrite:   aws.Bool(false),
	})
	var exists *ssmtypes.ParameterAlreadyExists
	switch {
	case err == nil:
		return minted, nil
	case !errors.As(err, &exists):
		return "", fmt.Errorf("write %s: %w", name, err)
	}
	token, found, err = readKVToken(ctx, params, name)
	if err != nil {
		return "", fmt.Errorf("read %s a concurrent deploy created: %w", name, err)
	}
	if !found {
		return "", fmt.Errorf("%s was created by a concurrent deploy and is gone again", name)
	}
	return token, nil
}

func previewKVToken(ctx context.Context, params ParametersAPI, name string) (string, error) {
	token, found, err := readKVToken(ctx, params, name)
	if err != nil || found {
		return token, err
	}
	return mintKVToken()
}

func deleteKVTokens(ctx context.Context, params ParametersAPI, names []string) error {
	for _, name := range names {
		_, err := params.DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String(name)})
		var notFound *ssmtypes.ParameterNotFound
		if err != nil && !errors.As(err, &notFound) {
			return fmt.Errorf("delete %s: %w", name, err)
		}
	}
	return nil
}

func collectKVBinding(ctx context.Context, params ParametersAPI, name string, fields map[string]any) (*bindingsv1.Binding, error) {
	host, err := requireStringField(fields, name, outputKeyHost)
	if err != nil {
		return nil, err
	}
	parameter, err := requireStringField(fields, name, outputKeyAuthTokenParameter)
	if err != nil {
		return nil, err
	}
	port := kvstore.ValkeyPort
	if p, ok := fields[outputKeyPort].(float64); ok {
		port = int(p)
	}
	token, found, err := readKVToken(ctx, params, parameter)
	if err != nil {
		return nil, fmt.Errorf("read the AUTH token for %s: %w", name, err)
	}
	if !found {
		return nil, fmt.Errorf("kv %s demands the AUTH token in %s, and that parameter is gone; re-deploy to mint a new one", name, parameter)
	}
	return &bindingsv1.Binding{
		Name: name,
		Properties: &bindingsv1.Binding_Kv{Kv: &bindingsv1.KvProperties{
			Host:     host,
			Port:     int32(port),
			Password: token,
			Tls:      true,
		}},
	}, nil
}
