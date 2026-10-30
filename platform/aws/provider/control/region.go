package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	"github.com/aws/smithy-go/middleware"

	"github.com/ocelhq/ocel/pkg/provider/spelling"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	regionsDoc        = "https://docs.aws.amazon.com/global-infrastructure/latest/regions/aws-regions.html"
	enablingRegionDoc = "https://docs.aws.amazon.com/accounts/latest/reference/manage-acct-regions.html"

	notOptedIn        = "not-opted-in"
	unknownRegionCode = "InvalidParameterValue"
)

var implicitGlobalRegions = []struct{ prefix, region string }{
	{prefix: "cn-", region: "cn-northwest-1"},
	{prefix: "us-gov-", region: "us-gov-west-1"},
	{prefix: "eusc-", region: "eusc-de-east-1"},
}

const commercialGlobalRegion = "us-east-1"

var regionVariables = []string{"AWS_REGION", "AWS_DEFAULT_REGION"}

type regionCheck struct {
	cfg    aws.Config
	source string
	once   sync.Once
	err    error
}

func addRegionCheck(cfg aws.Config, option string) aws.Config {
	if cfg.BaseEndpoint != nil || cfg.Region == "" {
		return cfg
	}
	check := &regionCheck{cfg: cfg.Copy(), source: findRegionSource(option)}
	checked := cfg.Copy()
	checked.APIOptions = append(slices.Clone(cfg.APIOptions), check.addTo)
	return checked
}

func findRegionSource(option string) string {
	if option != "" {
		return fmt.Sprintf("option %q", "region")
	}
	for _, variable := range regionVariables {
		if os.Getenv(variable) != "" {
			return variable
		}
	}
	return "the profile"
}

func (c *regionCheck) addTo(stack *middleware.Stack) error {
	return stack.Initialize.Add(middleware.InitializeMiddlewareFunc("OcelRegionCheck", c.handleInitialize), middleware.Before)
}

func (c *regionCheck) handleInitialize(ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler) (middleware.InitializeOutput, middleware.Metadata, error) {
	c.once.Do(func() { c.err = c.refuseRegionNotOffered(ctx) })
	if c.err != nil {
		return middleware.InitializeOutput{}, middleware.Metadata{}, c.err
	}
	return next.HandleInitialize(ctx, in)
}

func (c *regionCheck) refuseRegionNotOffered(ctx context.Context) error {
	from := c.cfg.Region
	described, answered := c.describeRegion(ctx, from)
	if !answered {
		from = implicitGlobalRegion(c.cfg.Region)
		if described, answered = c.describeRegion(ctx, from); !answered {
			return nil
		}
	}
	if described != nil {
		return c.refuseRegionNotEnabled(*described)
	}
	listed, err := c.listRegions(ctx, from)
	if err != nil {
		return refusal.RefuseOption(refusal.CodeInvalid, "%s names %q, which is not a region AWS offers. See %s",
			c.source, c.cfg.Region, regionsDoc)
	}
	if found, ok := findRegion(listed, c.cfg.Region); ok {
		return c.refuseRegionNotEnabled(found)
	}
	names := make([]string, 0, len(listed))
	for _, region := range listed {
		names = append(names, aws.ToString(region.RegionName))
	}
	slices.Sort(names)
	return refusal.RefuseOption(refusal.CodeInvalid, "%s names %q, which is not a region AWS offers.%s Regions: %s. See %s",
		c.source, c.cfg.Region, spelling.SuggestClosest(c.cfg.Region, names), strings.Join(names, ", "), regionsDoc)
}

func (c *regionCheck) refuseRegionNotEnabled(region ec2types.Region) error {
	if aws.ToString(region.OptInStatus) != notOptedIn {
		return nil
	}
	return refusal.RefuseOption(refusal.CodeNotReady,
		"%s names %q, a region this account has not enabled. Enable it, then try again: %s", c.source, c.cfg.Region, enablingRegionDoc)
}

func (c *regionCheck) describeRegion(ctx context.Context, from string) (*ec2types.Region, bool) {
	out, err := c.ec2In(from).DescribeRegions(ctx, &ec2.DescribeRegionsInput{AllRegions: aws.Bool(true), RegionNames: []string{c.cfg.Region}})
	var apiErr smithy.APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.ErrorCode() == unknownRegionCode:
		return nil, true
	case err != nil:
		return nil, false
	}
	if found, ok := findRegion(out.Regions, c.cfg.Region); ok {
		return &found, true
	}
	return nil, true
}

func (c *regionCheck) listRegions(ctx context.Context, from string) ([]ec2types.Region, error) {
	out, err := c.ec2In(from).DescribeRegions(ctx, &ec2.DescribeRegionsInput{AllRegions: aws.Bool(true)})
	if err != nil {
		return nil, err
	}
	return out.Regions, nil
}

func (c *regionCheck) ec2In(region string) *ec2.Client {
	return ec2.NewFromConfig(c.cfg, func(o *ec2.Options) { o.Region = region })
}

func findRegion(regions []ec2types.Region, name string) (ec2types.Region, bool) {
	for _, region := range regions {
		if aws.ToString(region.RegionName) == name {
			return region, true
		}
	}
	return ec2types.Region{}, false
}

func implicitGlobalRegion(region string) string {
	for _, partition := range implicitGlobalRegions {
		if strings.HasPrefix(region, partition.prefix) {
			return partition.region
		}
	}
	return commercialGlobalRegion
}
