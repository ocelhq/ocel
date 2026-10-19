package containers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

const (
	vpcOriginFailed     = "Failed"
	vpcOriginGonePoll   = 5 * time.Second
	vpcOriginGoneWithin = 15 * time.Minute
)

func reclaimFailedVPCOrigins(ctx context.Context, api awsports.VPCOriginsAPI, tier environment.Tier, progress progress.Log) error {
	failed, err := failedVPCOrigins(ctx, api, tier)
	if err != nil {
		return err
	}
	for _, id := range failed {
		if progress != nil {
			progress.Say("Removing VPC origin " + id + ", which a failed deploy of the " + string(tier) + " tier left behind under the name this one needs")
		}
		if err := deleteVPCOrigin(ctx, api, id); err != nil {
			return err
		}
	}
	return nil
}

func failedVPCOrigins(ctx context.Context, api awsports.VPCOriginsAPI, tier environment.Tier) ([]string, error) {
	var failed []string
	var marker *string
	for {
		page, err := api.ListVpcOrigins(ctx, &cloudfront.ListVpcOriginsInput{Marker: marker})
		if err != nil {
			return nil, fmt.Errorf("list the VPC origins in this account: %w", err)
		}
		if page.VpcOriginList == nil {
			return failed, nil
		}
		for _, origin := range page.VpcOriginList.Items {
			if aws.ToString(origin.Status) != vpcOriginFailed {
				continue
			}
			ours, err := taggedAsStack(ctx, api, aws.ToString(origin.Arn), tier)
			if err != nil {
				return nil, err
			}
			if ours {
				failed = append(failed, aws.ToString(origin.Id))
			}
		}
		if !aws.ToBool(page.VpcOriginList.IsTruncated) {
			return failed, nil
		}
		marker = page.VpcOriginList.NextMarker
	}
}

func taggedAsStack(ctx context.Context, api awsports.VPCOriginsAPI, arn string, tier environment.Tier) (bool, error) {
	out, err := api.ListTagsForResource(ctx, &cloudfront.ListTagsForResourceInput{Resource: aws.String(arn)})
	if err != nil {
		return false, fmt.Errorf("read the tags of VPC origin %s: %w", arn, err)
	}
	tags := map[string]string{}
	if out.Tags != nil {
		for _, tag := range out.Tags.Items {
			tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
		}
	}
	for key, value := range stackTags(tier) {
		if tags[key] != value {
			return false, nil
		}
	}
	return true, nil
}

func deleteVPCOrigin(ctx context.Context, api awsports.VPCOriginsAPI, id string) error {
	current, err := api.GetVpcOrigin(ctx, &cloudfront.GetVpcOriginInput{Id: aws.String(id)})
	if err != nil {
		return fmt.Errorf("read VPC origin %s before removing it: %w", id, err)
	}
	if _, err := api.DeleteVpcOrigin(ctx, &cloudfront.DeleteVpcOriginInput{Id: aws.String(id), IfMatch: current.ETag}); err != nil {
		return fmt.Errorf("remove VPC origin %s: %w", id, err)
	}
	deadline := time.Now().Add(vpcOriginGoneWithin)
	for {
		_, err := api.GetVpcOrigin(ctx, &cloudfront.GetVpcOriginInput{Id: aws.String(id)})
		var gone *cftypes.EntityNotFound
		if errors.As(err, &gone) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("wait for VPC origin %s to be removed: %w", id, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("VPC origin %s was still being removed after %s", id, vpcOriginGoneWithin)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(vpcOriginGonePoll):
		}
	}
}
