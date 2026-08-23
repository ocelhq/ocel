package server

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/domains"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (s *Server) domainState(ctx context.Context, region string, preview bool) (domains.State, error) {
	if s.openState != nil {
		return s.openState(ctx, region, preview)
	}
	awscfg, err := loadAWS(ctx, region)
	if err != nil {
		return domains.State{}, err
	}
	deployed, err := s.deployed(ctx, cloudformation.NewFromConfig(awscfg), region, preview)
	if err != nil {
		return domains.State{}, err
	}
	return recordState(awscfg, deployed), nil
}

func (s *Server) stackRecord(ctx context.Context, region string, class edge.Class, slug string) (domains.StackRecord, error) {
	state, err := s.domainState(ctx, region, class == edge.ClassPreview)
	if err != nil {
		return domains.StackRecord{}, err
	}
	return state.ReadStack(ctx, class, slug)
}

func recordState(awscfg aws.Config, deployed bootstrap.Deployed) domains.State {
	return domains.State{Records: awsports.Records{
		Dynamo: dynamodb.NewFromConfig(awscfg),
		Table:  deployed.StateTable,
	}}
}
