package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	RootStackStateParamPrefix        = "/ocel/rootstack/"
	PreviewRootStackStateParamPrefix = "/ocel/rootstack-preview/"
)

func rootStackStateParamPrefixFor(class string) (string, error) {
	switch class {
	case ClassProduction:
		return RootStackStateParamPrefix, nil
	case ClassPreview:
		return PreviewRootStackStateParamPrefix, nil
	default:
		return "", fmt.Errorf("root-stack state: unknown substrate class %q", class)
	}
}

func rootStackStateParamName(prefix, slug string) string {
	return prefix + slug
}

func rootStackStateParamDescription(class, slug string) string {
	return fmt.Sprintf(
		"Ocel: what the %s edge was left holding for project %q - the handles Ocel needs to recognise, update and eventually remove the root worker fronting this project. Written by every deploy of %q and read by the next deploy and by teardown. Delete it and Ocel loses track of edge resources it has already created for %q: they keep serving traffic, and teardown will not reclaim them.",
		class, slug, slug, slug,
	)
}

func WriteRootStackStateFor(ctx context.Context, ssmClient SSMAPI, class, slug string, state edge.RootStackState) error {
	prefix, err := rootStackStateParamPrefixFor(class)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal root-stack state: %w", err)
	}
	if _, err := ssmClient.PutParameter(ctx, &ssm.PutParameterInput{
		Name:        aws.String(rootStackStateParamName(prefix, slug)),
		Description: aws.String(rootStackStateParamDescription(class, slug)),
		Value:       aws.String(string(payload)),
		Type:        ssmtypes.ParameterTypeSecureString,
		Overwrite:   aws.Bool(true),
	}); err != nil {
		return fmt.Errorf("write root-stack state parameter: %w", err)
	}
	return nil
}

func ReadRootStackState(ctx context.Context, ssmClient SSMAPI, slug string) (edge.RootStackState, error) {
	return ReadRootStackStateFor(ctx, ssmClient, ClassProduction, slug)
}

func ReadRootStackStateFor(ctx context.Context, ssmClient SSMAPI, class, slug string) (edge.RootStackState, error) {
	prefix, err := rootStackStateParamPrefixFor(class)
	if err != nil {
		return nil, err
	}
	out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(rootStackStateParamName(prefix, slug)),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		var notFound *ssmtypes.ParameterNotFound
		if errors.As(err, &notFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("read root-stack state parameter: %w", err)
	}
	var state edge.RootStackState
	if err := json.Unmarshal([]byte(aws.ToString(out.Parameter.Value)), &state); err != nil {
		return nil, fmt.Errorf("parse root-stack state: %w", err)
	}
	return state, nil
}

func DeleteRootStackState(ctx context.Context, ssmClient SSMAPI, slug string) error {
	return DeleteRootStackStateFor(ctx, ssmClient, ClassProduction, slug)
}

func DeleteRootStackStateFor(ctx context.Context, ssmClient SSMAPI, class, slug string) error {
	prefix, err := rootStackStateParamPrefixFor(class)
	if err != nil {
		return err
	}
	if _, err := ssmClient.DeleteParameter(ctx, &ssm.DeleteParameterInput{
		Name: aws.String(rootStackStateParamName(prefix, slug)),
	}); err != nil {
		var notFound *ssmtypes.ParameterNotFound
		if errors.As(err, &notFound) {
			return nil
		}
		return fmt.Errorf("delete root-stack state parameter: %w", err)
	}
	return nil
}
