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

// RootStackStateParamPrefix is the SSM SecureString parameter path prefix a
// project's root-stack state (ADR 0001) is stored under, one parameter per
// project: RootStackStateParamPrefix + projectID. Root stacks exist only for
// production, so unlike the edge params there is no preview counterpart.
const RootStackStateParamPrefix = "/ocel/rootstack/"

func rootStackStateParamName(projectID string) string {
	return RootStackStateParamPrefix + projectID
}

// WriteRootStackState persists a project's root-stack state so the next
// production deploy — and rollback/deployments-ls — reconcile against it
// instead of reconciling from scratch. It is the project's current state and
// is overwritten on every deploy, exactly like writeEdgeValues.
func WriteRootStackState(ctx context.Context, ssmClient SSMAPI, projectID string, state edge.RootStackState) error {
	payload, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal root-stack state: %w", err)
	}
	if _, err := ssmClient.PutParameter(ctx, &ssm.PutParameterInput{
		Name:      aws.String(rootStackStateParamName(projectID)),
		Value:     aws.String(string(payload)),
		Type:      ssmtypes.ParameterTypeSecureString,
		Overwrite: aws.Bool(true),
	}); err != nil {
		return fmt.Errorf("write root-stack state parameter: %w", err)
	}
	return nil
}

// ReadRootStackState returns a project's stored root-stack state, decrypted. A
// project that has never produced one (no production deploy yet) reads as nil
// rather than as a failure, which ReconcileRootStack reads as "reconcile from
// scratch".
func ReadRootStackState(ctx context.Context, ssmClient SSMAPI, projectID string) (edge.RootStackState, error) {
	out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(rootStackStateParamName(projectID)),
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
