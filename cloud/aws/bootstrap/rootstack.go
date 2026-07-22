package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/ocelhq/ocel/cloud/edge"
)

// RootStackStateParamPrefix / PreviewRootStackStateParamPrefix are the SSM
// SecureString parameter path prefixes a project's root-stack state (ADR 0001)
// is stored under, one parameter per project: prefix + projectID. Production
// and preview each keep their own root-stack state (their own store instance,
// secret and owner token), so the two never share a parameter.
const (
	RootStackStateParamPrefix        = "/ocel/rootstack/"
	PreviewRootStackStateParamPrefix = "/ocel/rootstack-preview/"
)

// rootStackStateParamPrefixFor selects the parameter prefix for a substrate
// class. It is pure.
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

func rootStackStateParamName(prefix, projectID string) string {
	return prefix + projectID
}

// WriteRootStackState persists a production project's root-stack state.
func WriteRootStackState(ctx context.Context, ssmClient SSMAPI, projectID string, state edge.RootStackState) error {
	return WriteRootStackStateFor(ctx, ssmClient, ClassProduction, projectID, state)
}

// WriteRootStackStateFor persists a project's root-stack state for a substrate
// class so the next deploy — and rollback/deployments-ls — reconcile against it
// instead of reconciling from scratch. It is the project's current state and is
// overwritten on every deploy, exactly like writeEdgeValues.
func WriteRootStackStateFor(ctx context.Context, ssmClient SSMAPI, class, projectID string, state edge.RootStackState) error {
	prefix, err := rootStackStateParamPrefixFor(class)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal root-stack state: %w", err)
	}
	if _, err := ssmClient.PutParameter(ctx, &ssm.PutParameterInput{
		Name:      aws.String(rootStackStateParamName(prefix, projectID)),
		Value:     aws.String(string(payload)),
		Type:      ssmtypes.ParameterTypeSecureString,
		Overwrite: aws.Bool(true),
	}); err != nil {
		return fmt.Errorf("write root-stack state parameter: %w", err)
	}
	return nil
}

// ReadRootStackState returns a production project's stored root-stack state.
func ReadRootStackState(ctx context.Context, ssmClient SSMAPI, projectID string) (edge.RootStackState, error) {
	return ReadRootStackStateFor(ctx, ssmClient, ClassProduction, projectID)
}

// ReadRootStackStateFor returns a project's stored root-stack state for a
// substrate class, decrypted. A project that has never produced one reads as
// nil rather than as a failure, which ReconcileRootStack reads as "reconcile
// from scratch".
func ReadRootStackStateFor(ctx context.Context, ssmClient SSMAPI, class, projectID string) (edge.RootStackState, error) {
	prefix, err := rootStackStateParamPrefixFor(class)
	if err != nil {
		return nil, err
	}
	out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(rootStackStateParamName(prefix, projectID)),
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

// DeleteRootStackState removes a production project's stored root-stack state.
func DeleteRootStackState(ctx context.Context, ssmClient SSMAPI, projectID string) error {
	return DeleteRootStackStateFor(ctx, ssmClient, ClassProduction, projectID)
}

// DeleteRootStackStateFor removes a project's stored root-stack state for a
// substrate class, the last step of `ocel destroy` once the root stack itself
// is gone. A project that has no stored state (already deleted, or never
// deployed) is treated as success so destroy stays idempotent.
func DeleteRootStackStateFor(ctx context.Context, ssmClient SSMAPI, class, projectID string) error {
	prefix, err := rootStackStateParamPrefixFor(class)
	if err != nil {
		return err
	}
	if _, err := ssmClient.DeleteParameter(ctx, &ssm.DeleteParameterInput{
		Name: aws.String(rootStackStateParamName(prefix, projectID)),
	}); err != nil {
		var notFound *ssmtypes.ParameterNotFound
		if errors.As(err, &notFound) {
			return nil
		}
		return fmt.Errorf("delete root-stack state parameter: %w", err)
	}
	return nil
}
