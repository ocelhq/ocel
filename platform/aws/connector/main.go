package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"

	"github.com/ocelhq/ocel/pkg/connectorkit"
	"github.com/ocelhq/ocel/pkg/providerkit"
	kit "github.com/ocelhq/ocel/pkg/providerkit/ports"
	"github.com/ocelhq/ocel/pkg/target"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

var version = "dev"

const runtimeAPIEnvVar = "AWS_LAMBDA_RUNTIME_API"

func main() {
	addr := flag.String("addr", "127.0.0.1:7777", "address to serve on")
	region := flag.String("region", os.Getenv(edge.AWSRegionVar), "AWS region the target is bootstrapped in")
	config := flag.String("config", os.Getenv("OCEL_CONNECTOR_CONFIG"), "path to the connector config naming the console, this connector and its grants")
	flag.Parse()

	if err := run(*addr, *region, *config); err != nil {
		fmt.Fprintln(os.Stderr, "ocel connector:", err)
		os.Exit(1)
	}
}

func run(addr, region, config string) error {
	ctx := context.Background()

	trust, err := connectorkit.Configured(config)
	if err != nil {
		return err
	}

	ns, err := providerkit.NamespaceFromEnv()
	if err != nil {
		return err
	}
	cfg, err := sdkconfig.Control(ctx, region)
	if err != nil {
		return err
	}

	who, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return fmt.Errorf("resolve AWS account id: %w", err)
	}
	trust.Target, err = target.Fingerprint("aws", aws.ToString(who.Account), cfg.Region, string(ns))
	if err != nil {
		return err
	}

	held := &deployments{
		namespace: bootstrap.Namespace(ns),
		stacks:    cloudformation.NewFromConfig(cfg),
		now:       time.Now,
		read:      map[kit.Class]readDeployment{},
	}

	spec := connectorkit.Spec{
		Config:     trust,
		Version:    version,
		Vendor:     "aws",
		Addr:       addr,
		ConfigPath: config,
		Vars: providerkit.Vars{
			Records: awsports.Records{Dynamo: dynamodb.NewFromConfig(cfg), Tables: held},
			Sealer:  awsports.Sealer{KMS: kms.NewFromConfig(cfg), Keys: held},
		},
	}

	if os.Getenv(runtimeAPIEnvVar) == "" {
		return connectorkit.Serve(spec)
	}

	mux, err := connectorkit.Mux(spec)
	if err != nil {
		return err
	}
	lambda.StartWithOptions(httpadapter.NewV2(mux).ProxyWithContext, lambda.WithContext(ctx))
	return nil
}

const deploymentsTTL = 5 * time.Minute

type deployments struct {
	namespace bootstrap.Namespace
	stacks    cfn.Describer
	now       func() time.Time

	mu   sync.Mutex
	read map[kit.Class]readDeployment
}

type readDeployment struct {
	held bootstrap.Deployed
	at   time.Time
}

func (d *deployments) resolve(ctx context.Context, class kit.Class) (bootstrap.Deployed, error) {
	d.mu.Lock()
	memo, known := d.read[class]
	d.mu.Unlock()
	if known && d.now().Sub(memo.at) < deploymentsTTL {
		return memo.held, nil
	}
	held, err := bootstrap.CheckDeployedFor(ctx, d.stacks, d.namespace, string(class))
	if err != nil {
		return bootstrap.Deployed{}, err
	}
	d.mu.Lock()
	d.read[class] = readDeployment{held: held, at: d.now()}
	d.mu.Unlock()
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
