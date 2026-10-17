package envsourcesync

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedulertypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrapfunction"
)

func Remove(ctx context.Context, c Clients, namespace string, tier environment.Tier) error {
	names := NamesFor(namespace, tier)
	var missingSchedule *schedulertypes.ResourceNotFoundException
	if _, err := c.Scheduler.DeleteSchedule(ctx, &scheduler.DeleteScheduleInput{Name: aws.String(names.Schedule), GroupName: aws.String(names.ScheduleGroup)}); err != nil && !errors.As(err, &missingSchedule) {
		return fmt.Errorf("delete schedule %s: %w", names.Schedule, err)
	}
	if _, err := c.Scheduler.DeleteScheduleGroup(ctx, &scheduler.DeleteScheduleGroupInput{Name: aws.String(names.ScheduleGroup)}); err != nil && !errors.As(err, &missingSchedule) {
		return fmt.Errorf("delete schedule group %s: %w", names.ScheduleGroup, err)
	}
	if err := bootstrapfunction.Remove(ctx, c.functions(), names.Function, names.LogGroup); err != nil {
		return err
	}
	for _, role := range []string{names.ScheduleRole, names.Role} {
		if err := bootstrapfunction.RemoveRole(ctx, c.IAM, role); err != nil {
			return err
		}
	}
	return nil
}
