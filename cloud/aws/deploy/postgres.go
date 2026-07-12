package deploy

import (
	ec2 "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	rds "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/rds"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// Aurora Serverless v2 defaults for a postgres resource. These are
// provider-chosen this slice: PostgresConfig only carries a version, so
// capacity, credentials, and teardown behaviour are fixed here.
const (
	postgresEngine               = "aurora-postgresql"
	postgresEngineMode           = "provisioned" // serverless v2 runs in provisioned mode + a scaling config
	defaultPostgresEngineVersion = "16.4"
	postgresMinCapacity          = 0.0 // scale to zero when idle
	postgresMaxCapacity          = 2.0
	postgresInstanceClass        = "db.serverless"
	postgresPort                 = 5432
	postgresMasterUsername       = "ocel"
	postgresDatabaseName         = "ocel"
)

// postgresArgs is the fully-resolved set of Aurora Serverless v2 arguments a
// PostgresConfig lowers to, independent of any Pulumi or AWS call. It is the
// pure output of translatePostgres so the translation can be unit-tested
// without provisioning anything.
type postgresArgs struct {
	Engine               string
	EngineMode           string
	EngineVersion        string
	MinCapacity          float64
	MaxCapacity          float64
	InstanceClass        string
	Port                 int
	MasterUsername       string
	DatabaseName         string
	ManageMasterPassword bool
	PubliclyAccessible   bool
	DeletionProtection   bool
	SkipFinalSnapshot    bool
}

// translatePostgres lowers a PostgresConfig into the concrete Aurora
// Serverless v2 arguments the provider provisions. It is pure: same config
// in, same args out, no I/O. An empty version falls back to a pinned recent
// engine version.
func translatePostgres(cfg *resourcesv1.PostgresConfig) postgresArgs {
	version := defaultPostgresEngineVersion
	if v := cfg.GetVersion(); v != "" {
		version = v
	}
	return postgresArgs{
		Engine:               postgresEngine,
		EngineMode:           postgresEngineMode,
		EngineVersion:        version,
		MinCapacity:          postgresMinCapacity,
		MaxCapacity:          postgresMaxCapacity,
		InstanceClass:        postgresInstanceClass,
		Port:                 postgresPort,
		MasterUsername:       postgresMasterUsername,
		DatabaseName:         postgresDatabaseName,
		ManageMasterPassword: true,
		PubliclyAccessible:   false,
		DeletionProtection:   false,
		SkipFinalSnapshot:    true,
	}
}

// registerPostgres declares the Aurora Serverless v2 cluster (plus its subnet
// group, security group, and serverless instance) for one postgres resource
// inside a Pulumi program, and exports the resource's connection outputs
// under logicalName. vpcID/vpcCIDR/subnetIDs identify the default VPC the
// cluster lands in. The exported map carries the discrete connection parts
// plus the RDS-managed master-password secret ARN, which the caller resolves
// to a plaintext password after the stack settles (see collectPostgresOutput).
func registerPostgres(ctx *pulumi.Context, logicalName string, args postgresArgs, vpcID, vpcCIDR string, subnetIDs []string) (pulumi.StringOutput, error) {
	sg, err := ec2.NewSecurityGroup(ctx, logicalName+"-sg", &ec2.SecurityGroupArgs{
		Description: pulumi.String("Ocel-managed security group for " + logicalName),
		VpcId:       pulumi.String(vpcID),
		// The cluster is private (publiclyAccessible=false). Allow the Postgres
		// port only from within the VPC — that's where the deployed app runs —
		// and never from the public internet. Egress is open so the DB can
		// reach AWS services it needs.
		Ingress: ec2.SecurityGroupIngressArray{
			&ec2.SecurityGroupIngressArgs{
				Protocol:    pulumi.String("tcp"),
				FromPort:    pulumi.Int(args.Port),
				ToPort:      pulumi.Int(args.Port),
				CidrBlocks:  pulumi.StringArray{pulumi.String(vpcCIDR)},
				Description: pulumi.String("Postgres access from within the VPC"),
			},
		},
		Egress: ec2.SecurityGroupEgressArray{
			&ec2.SecurityGroupEgressArgs{
				Protocol:   pulumi.String("-1"),
				FromPort:   pulumi.Int(0),
				ToPort:     pulumi.Int(0),
				CidrBlocks: pulumi.StringArray{pulumi.String("0.0.0.0/0")},
			},
		},
	})
	if err != nil {
		return pulumi.StringOutput{}, err
	}

	// RDS identifiers (subnet group, cluster, instance) forbid the underscores
	// the logical name carries, so name each from a safe prefix rather than
	// Pulumi's autoname.
	subnetGroup, err := rds.NewSubnetGroup(ctx, logicalName+"-subnets", &rds.SubnetGroupArgs{
		NamePrefix: pulumi.String(physicalNamePrefix(logicalName, "subnets")),
		SubnetIds:  pulumi.ToStringArray(subnetIDs),
	})
	if err != nil {
		return pulumi.StringOutput{}, err
	}

	cluster, err := rds.NewCluster(ctx, logicalName, &rds.ClusterArgs{
		ClusterIdentifierPrefix:  pulumi.String(physicalNamePrefix(logicalName, "")),
		Engine:                   pulumi.String(args.Engine),
		EngineMode:               pulumi.String(args.EngineMode),
		EngineVersion:            pulumi.String(args.EngineVersion),
		DatabaseName:             pulumi.String(args.DatabaseName),
		MasterUsername:           pulumi.String(args.MasterUsername),
		ManageMasterUserPassword: pulumi.Bool(args.ManageMasterPassword),
		DbSubnetGroupName:        subnetGroup.Name,
		VpcSecurityGroupIds:      pulumi.StringArray{sg.ID()},
		DeletionProtection:       pulumi.Bool(args.DeletionProtection),
		SkipFinalSnapshot:        pulumi.Bool(args.SkipFinalSnapshot),
		Serverlessv2ScalingConfiguration: &rds.ClusterServerlessv2ScalingConfigurationArgs{
			MinCapacity: pulumi.Float64(args.MinCapacity),
			MaxCapacity: pulumi.Float64(args.MaxCapacity),
		},
	})
	if err != nil {
		return pulumi.StringOutput{}, err
	}

	_, err = rds.NewClusterInstance(ctx, logicalName+"-instance", &rds.ClusterInstanceArgs{
		IdentifierPrefix:   pulumi.String(physicalNamePrefix(logicalName, "instance")),
		ClusterIdentifier:  cluster.ID(),
		Engine:             rds.EngineType(args.Engine),
		EngineVersion:      cluster.EngineVersion,
		InstanceClass:      pulumi.String(args.InstanceClass),
		PubliclyAccessible: pulumi.Bool(args.PubliclyAccessible),
	})
	if err != nil {
		return pulumi.StringOutput{}, err
	}

	secretARN := cluster.MasterUserSecrets.Index(pulumi.Int(0)).SecretArn()
	ctx.Export(logicalName, pulumi.Map{
		outputKeyHost:      cluster.Endpoint,
		outputKeyPort:      cluster.Port,
		outputKeyDatabase:  pulumi.String(args.DatabaseName),
		outputKeyUsername:  cluster.MasterUsername,
		outputKeySecretARN: secretARN,
	})

	return postgresEnvValue(ctx, cluster.MasterUsername, cluster.Endpoint, cluster.Port, args.DatabaseName, secretARN.Elem()), nil
}

// Keys of the per-resource output map exported by registerPostgres and read
// back by collectPostgresOutput.
const (
	outputKeyHost      = "host"
	outputKeyPort      = "port"
	outputKeyDatabase  = "database"
	outputKeyUsername  = "username"
	outputKeySecretARN = "secretArn"
)
