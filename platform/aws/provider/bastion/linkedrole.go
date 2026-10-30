package bastion

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
)

const (
	ecsPrincipal          = "ecs.amazonaws.com"
	linkedRoleTaken       = "has been taken in this account"
	linkedRoleUnassumable = "unable to assume the service linked role"
)

func (c Clients) ensureLinkedRole(ctx context.Context) error {
	_, err := c.IAM.CreateServiceLinkedRole(ctx, &iam.CreateServiceLinkedRoleInput{AWSServiceName: aws.String(ecsPrincipal)})
	var invalid *iamtypes.InvalidInputException
	if err == nil || errors.As(err, &invalid) && strings.Contains(invalid.ErrorMessage(), linkedRoleTaken) {
		return nil
	}
	return fmt.Errorf("create the service-linked role ECS starts tasks with: %w", err)
}
