package postgres

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

const reasonProtectionDisabled = "its deletion protection is disabled first, and a final snapshot keeps its data"

type ClustersAPI interface {
	ModifyDBCluster(ctx context.Context, in *rds.ModifyDBClusterInput, optFns ...func(*rds.Options)) (*rds.ModifyDBClusterOutput, error)
}

func isProtectedTeardown(ref provider.StackRef) bool {
	return ref.Tier == environment.TierProduction && ref.Name.IsInfra()
}

func markProtectedClusters(plan provider.Plan, ref provider.StackRef) provider.Plan {
	if !isProtectedTeardown(ref) {
		return plan
	}
	for _, group := range plan.Groups {
		for i, change := range group.Changes {
			if change.Kind != component.TokenRDSCluster || change.Action != provider.ActionDelete {
				continue
			}
			group.Changes[i].Action = provider.ActionDisableThenDelete
			group.Changes[i].Reason = reasonProtectionDisabled
			group.Changes[i].Slow = true
		}
	}
	return plan
}

func disableDeletionProtection(ctx context.Context, clusters ClustersAPI, ref provider.StackRef, outputsOf func(context.Context) (auto.OutputMap, error), progress progress.Log) error {
	if !isProtectedTeardown(ref) {
		return nil
	}
	outputs, err := outputsOf(ctx)
	if err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(outputs)) {
		fields, _ := outputs[name].Value.(map[string]any)
		cluster, _ := fields[OutputKeyClusterIdentifier].(string)
		if cluster == "" {
			continue
		}
		if clusters == nil {
			return errors.New("no RDS client configured")
		}
		if progress != nil {
			progress.Say("Disabling the deletion protection of " + cluster + ", the " + name + " database")
		}
		_, err := clusters.ModifyDBCluster(ctx, &rds.ModifyDBClusterInput{
			DBClusterIdentifier: aws.String(cluster),
			DeletionProtection:  aws.Bool(false),
			ApplyImmediately:    aws.Bool(true),
		})
		var gone *rdstypes.DBClusterNotFoundFault
		if errors.As(err, &gone) {
			continue
		}
		if err != nil {
			return fmt.Errorf("disable the deletion protection of %s, the %s database: %w", cluster, name, err)
		}
	}
	return nil
}
