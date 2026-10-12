package tasks

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
)

func (e *Engine) canScaleIn() bool {
	return e.cfg.Topology.Workers[e.cfg.Worker].Service != nil && e.cfg.Services != nil
}

func (e *Engine) scaleInToFloor(ctx context.Context) error {
	service := e.cfg.Topology.Workers[e.cfg.Worker].Service
	_, err := e.cfg.Services.UpdateService(ctx, &ecs.UpdateServiceInput{Cluster: aws.String(service.Cluster), Service: aws.String(service.Name), DesiredCount: aws.Int32(int32(service.Floor))})
	return err
}
