package tasks

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

func NewAWS(cfg aws.Config, topology queues.Topology, worker, workerURL string) *Engine {
	return New(Config{
		Topology:  topology,
		Table:     dynamodb.NewFromConfig(cfg),
		Queues:    sqs.NewFromConfig(cfg),
		Topics:    sns.NewFromConfig(cfg),
		Services:  ecs.NewFromConfig(cfg),
		Worker:    worker,
		WorkerURL: workerURL,
	})
}
