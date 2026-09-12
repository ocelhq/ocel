package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/ocelhq/ocel/pkg/connectorkit"
	"github.com/ocelhq/ocel/pkg/providerkit"
	kit "github.com/ocelhq/ocel/pkg/providerkit/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
)

const version = "0.0.0-alpha"

func main() {
	addr := flag.String("addr", "127.0.0.1:7777", "address to serve on")
	region := flag.String("region", os.Getenv("OCEL_AWS_REGION"), "AWS region the target is bootstrapped in")
	reveal := flag.Bool("reveal", false, "answer RevealValues, so the console can show plain and sensitive values")
	flag.Parse()

	if err := run(*addr, *region, *reveal); err != nil {
		fmt.Fprintln(os.Stderr, "ocel connector:", err)
		os.Exit(1)
	}
}

func run(addr, region string, reveal bool) error {
	ctx := context.Background()

	ns, err := providerkit.NamespaceFromEnv()
	if err != nil {
		return err
	}
	cfg, err := sdkconfig.Control(ctx, region)
	if err != nil {
		return err
	}

	held := &deployments{
		namespace: bootstrap.Namespace(ns),
		stacks:    cloudformation.NewFromConfig(cfg),
		read:      map[kit.Class]bootstrap.Deployed{},
	}

	return connectorkit.Serve(connectorkit.Spec{
		Version: version,
		Vendor:  "aws",
		Target:  fmt.Sprintf("aws/%s/%s", cfg.Region, ns),
		Addr:    addr,
		Reveal:  reveal,
		Vars: providerkit.Vars{
			Records: awsports.Records{Dynamo: dynamodb.NewFromConfig(cfg), Tables: held},
			Sealer:  awsports.Sealer{KMS: kms.NewFromConfig(cfg), Keys: held},
		},
	})
}

type deployments struct {
	namespace bootstrap.Namespace
	stacks    *cloudformation.Client

	mu   sync.Mutex
	read map[kit.Class]bootstrap.Deployed
}

func (d *deployments) resolve(ctx context.Context, class kit.Class) (bootstrap.Deployed, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if held, known := d.read[class]; known {
		return held, nil
	}
	held, err := bootstrap.CheckDeployedFor(ctx, d.stacks, d.namespace, string(class))
	if err != nil {
		return bootstrap.Deployed{}, err
	}
	d.read[class] = held
	return held, nil
}

func (d *deployments) Table(ctx context.Context, class kit.Class) (string, error) {
	held, err := d.resolve(ctx, class)
	return held.StateTable, err
}

func (d *deployments) ValuesTable(ctx context.Context, class kit.Class) (string, error) {
	held, err := d.resolve(ctx, class)
	return held.VarsTable, err
}

func (d *deployments) Key(ctx context.Context, class kit.Class) (string, error) {
	held, err := d.resolve(ctx, class)
	return held.VarsKeyARN, err
}
