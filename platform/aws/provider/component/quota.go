package component

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"
	sqtypes "github.com/aws/aws-sdk-go-v2/service/servicequotas/types"

	"github.com/ocelhq/ocel/pkg/refusal"
)

type QuotasAPI interface {
	GetServiceQuota(ctx context.Context, in *servicequotas.GetServiceQuotaInput, optFns ...func(*servicequotas.Options)) (*servicequotas.GetServiceQuotaOutput, error)
	GetAWSDefaultServiceQuota(ctx context.Context, in *servicequotas.GetAWSDefaultServiceQuotaInput, optFns ...func(*servicequotas.Options)) (*servicequotas.GetAWSDefaultServiceQuotaOutput, error)
}

type Quota struct {
	Service string
	Code    string
	Name    string
}

var (
	VPCQuota       = Quota{Service: "vpc", Code: "L-F678F1CE", Name: "VPCs per Region"}
	ElasticIPQuota = Quota{Service: "ec2", Code: "L-0263D0A3", Name: "EC2-VPC Elastic IPs"}
)

func (q Quota) ARN() string {
	return "arn:aws:servicequotas:*:*:" + q.Service + "/" + q.Code
}

func RefuseFullQuota(ctx context.Context, api QuotasAPI, quota Quota, region string, held, adding int, needs string) error {
	limit, listed, err := readQuota(ctx, api, quota)
	if err != nil || !listed {
		return err
	}
	if held+adding <= limit {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"%s needs %d more of the %q quota (%s), and this account holds %d of its %d in %s: free some, or raise it with "+
			"`aws service-quotas request-service-quota-increase --region %s --service-code %s --quota-code %s --desired-value %d` and run this again",
		needs, adding, quota.Name, quota.Code, held, limit, region, region, quota.Service, quota.Code, held+adding)
}

func readQuota(ctx context.Context, api QuotasAPI, quota Quota) (int, bool, error) {
	applied, err := api.GetServiceQuota(ctx, &servicequotas.GetServiceQuotaInput{
		ServiceCode: aws.String(quota.Service),
		QuotaCode:   aws.String(quota.Code),
	})
	var unlisted *sqtypes.NoSuchResourceException
	switch {
	case err == nil && applied.Quota != nil && applied.Quota.Value != nil:
		return int(math.Floor(*applied.Quota.Value)), true, nil
	case err != nil && !errors.As(err, &unlisted):
		return 0, false, fmt.Errorf("read the %q quota (%s): %w", quota.Name, quota.Code, err)
	}
	fallback, err := api.GetAWSDefaultServiceQuota(ctx, &servicequotas.GetAWSDefaultServiceQuotaInput{
		ServiceCode: aws.String(quota.Service),
		QuotaCode:   aws.String(quota.Code),
	})
	switch {
	case errors.As(err, &unlisted):
		return 0, false, nil
	case err != nil:
		return 0, false, fmt.Errorf("read the AWS default of the %q quota (%s): %w", quota.Name, quota.Code, err)
	case fallback.Quota == nil || fallback.Quota.Value == nil:
		return 0, false, fmt.Errorf("AWS gave no value for the %q quota (%s)", quota.Name, quota.Code)
	}
	return int(math.Floor(*fallback.Quota.Value)), true, nil
}
