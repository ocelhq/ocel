package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

func OriginSecretParamFor(class string) (string, error) {
	names, err := edgeNamesFor(class)
	if err != nil {
		return "", err
	}
	return names.originSecretParam, nil
}

func ensureOriginSecret(ctx context.Context, ssmClient SSMAPI, class string) (string, error) {
	paramName, err := OriginSecretParamFor(class)
	if err != nil {
		return "", err
	}
	return ensureSecret(ctx, ssmClient, paramName, fmt.Sprintf(
		"Ocel: the shared secret every %s release Lambda demands of the front that reaches it, because those Function URLs answer without SigV4. "+
			"Generated once; a release learns it as environment at deploy and the front learns it at promote, so rotating it means deleting it, "+
			"deploying every release that must keep serving, and promoting again.",
		class,
	))
}

func ensureSecret(ctx context.Context, ssmClient SSMAPI, paramName, description string) (string, error) {
	read := func() (string, error) {
		out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
			Name:           aws.String(paramName),
			WithDecryption: aws.Bool(true),
		})
		if err != nil {
			return "", err
		}
		return aws.ToString(out.Parameter.Value), nil
	}

	secret, err := read()
	if err == nil {
		return secret, nil
	}
	var notFound *ssmtypes.ParameterNotFound
	if !errors.As(err, &notFound) {
		return "", fmt.Errorf("read %s: %w", paramName, err)
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate the secret %s holds: %w", paramName, err)
	}
	minted := hex.EncodeToString(buf)
	if _, err := ssmClient.PutParameter(ctx, &ssm.PutParameterInput{
		Name:        aws.String(paramName),
		Description: aws.String(description),
		Value:       aws.String(minted),
		Type:        ssmtypes.ParameterTypeSecureString,
		Overwrite:   aws.Bool(false),
	}); err != nil {
		var exists *ssmtypes.ParameterAlreadyExists
		if !errors.As(err, &exists) {
			return "", fmt.Errorf("write %s: %w", paramName, err)
		}
		secret, err := read()
		if err != nil {
			return "", fmt.Errorf("read %s a concurrent bootstrap created: %w", paramName, err)
		}
		return secret, nil
	}
	return minted, nil
}
