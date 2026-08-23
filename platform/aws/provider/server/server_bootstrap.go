package server

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type gated struct {
	providerkit.Gate

	bootstrapper awsports.Bootstrapper
	deployed     bootstrap.Deployed
}

func (s *Server) gated(ctx context.Context, class string, front edge.Edge) (gated, error) {
	opts := s.config.get()
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return gated{}, err
	}
	return s.gatedOn(ctx, awscfg, class, front)
}

func (s *Server) gatedOn(ctx context.Context, awscfg aws.Config, class string, front edge.Edge) (gated, error) {
	bootstrapper := awsports.BootstrapperFor(awscfg, front, s.writer)
	deployed, err := s.deployed(ctx, bootstrapper.CFN, awscfg.Region, class == bootstrap.ClassPreview)
	if err != nil {
		return gated{}, err
	}
	return gated{
		Gate: providerkit.Gate{
			Bootstrapper: bootstrapper,
			Records:      awsports.Records{Dynamo: dynamodb.NewFromConfig(awscfg), Table: deployed.StateTable},
			Writer:       s.writer,
		},
		bootstrapper: bootstrapper,
		deployed:     deployed,
	}, nil
}

func (s *Server) applying(run func() error) error {
	if err := run(); err != nil {
		return err
	}
	s.memo.forgetDeployed()
	return nil
}

type reportTo struct {
	say    func(string)
	detail func(string)
}

func (r reportTo) Say(message string) {
	if r.say != nil {
		r.say(message)
	}
}

func (r reportTo) Detail(message string) {
	if r.detail != nil {
		r.detail(message)
	}
}

func (reportTo) Span(string, time.Time, time.Time, error, ...providerkit.Attr) {}

var _ providerkit.Reporter = reportTo{}
