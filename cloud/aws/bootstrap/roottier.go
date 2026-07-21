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

// RootTierStateParamPrefix is the SSM SecureString parameter path prefix a
// project's root-tier state (ADR 0001) is stored under, one parameter per
// project: RootTierStateParamPrefix + projectID. Root tiers exist only for
// production, so unlike the edge params there is no preview counterpart.
const RootTierStateParamPrefix = "/ocel/roottier/"

func rootTierStateParamName(projectID string) string {
	return RootTierStateParamPrefix + projectID
}

// WriteRootTierState persists a project's root-tier state so the next
// production deploy — and rollback/deployments-ls — reconcile against it
// instead of reconciling from scratch. It is the project's current state and
// is overwritten on every deploy, exactly like writeEdgeValues.
func WriteRootTierState(ctx context.Context, ssmClient SSMAPI, projectID string, state edge.RootTierState) error {
	payload, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal root-tier state: %w", err)
	}
	if _, err := ssmClient.PutParameter(ctx, &ssm.PutParameterInput{
		Name:      aws.String(rootTierStateParamName(projectID)),
		Value:     aws.String(string(payload)),
		Type:      ssmtypes.ParameterTypeSecureString,
		Overwrite: aws.Bool(true),
	}); err != nil {
		return fmt.Errorf("write root-tier state parameter: %w", err)
	}
	return nil
}

// ReadRootTierState returns a project's stored root-tier state, decrypted. A
// project that has never produced one (no production deploy yet) reads as nil
// rather than as a failure, which ReconcileRootTier reads as "reconcile from
// scratch".
func ReadRootTierState(ctx context.Context, ssmClient SSMAPI, projectID string) (edge.RootTierState, error) {
	out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(rootTierStateParamName(projectID)),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		var notFound *ssmtypes.ParameterNotFound
		if errors.As(err, &notFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("read root-tier state parameter: %w", err)
	}
	var state edge.RootTierState
	if err := json.Unmarshal([]byte(aws.ToString(out.Parameter.Value)), &state); err != nil {
		return nil, fmt.Errorf("parse root-tier state: %w", err)
	}
	return state, nil
}
