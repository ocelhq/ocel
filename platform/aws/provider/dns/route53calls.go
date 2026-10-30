package dns

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/route53"
)

type route53Calls struct {
	listHostedZones          func(context.Context, *route53.ListHostedZonesInput, ...func(*route53.Options)) (*route53.ListHostedZonesOutput, error)
	listResourceRecordSets   func(context.Context, *route53.ListResourceRecordSetsInput, ...func(*route53.Options)) (*route53.ListResourceRecordSetsOutput, error)
	changeResourceRecordSets func(context.Context, *route53.ChangeResourceRecordSetsInput, ...func(*route53.Options)) (*route53.ChangeResourceRecordSetsOutput, error)
}

func newRoute53Calls(client *route53.Client) route53Calls {
	return route53Calls{
		listHostedZones:          client.ListHostedZones,
		listResourceRecordSets:   client.ListResourceRecordSets,
		changeResourceRecordSets: client.ChangeResourceRecordSets,
	}
}

func (c route53Calls) ListHostedZones(ctx context.Context, in *route53.ListHostedZonesInput, optFns ...func(*route53.Options)) (*route53.ListHostedZonesOutput, error) {
	return c.listHostedZones(ctx, in, optFns...)
}

func (c route53Calls) ListResourceRecordSets(ctx context.Context, in *route53.ListResourceRecordSetsInput, optFns ...func(*route53.Options)) (*route53.ListResourceRecordSetsOutput, error) {
	return c.listResourceRecordSets(ctx, in, optFns...)
}

func (c route53Calls) ChangeResourceRecordSets(ctx context.Context, in *route53.ChangeResourceRecordSetsInput, optFns ...func(*route53.Options)) (*route53.ChangeResourceRecordSetsOutput, error) {
	return c.changeResourceRecordSets(ctx, in, optFns...)
}
