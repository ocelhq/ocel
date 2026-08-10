package deploy

import (
	ec2 "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	rds "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/rds"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

const (
	postgresEngine               = "aurora-postgresql"
	postgresEngineMode           = "provisioned"
	defaultPostgresEngineVersion = "16.4"
	postgresMinCapacity          = 0.0
	postgresMaxCapacity          = 2.0
	postgresInstanceClass        = "db.serverless"
	postgresPort                 = 5432
	postgresMasterUsername       = "ocel"
	postgresDatabaseName         = "ocel"
)

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

func registerPostgres(ctx *pulumi.Context, logicalName string, args postgresArgs, vpcID, vpcCIDR string, subnetIDs []string) (pulumi.StringOutput, error) {
	sg, err := ec2.NewSecurityGroup(ctx, logicalName+"-sg", &ec2.SecurityGroupArgs{
		Description: pulumi.String("Ocel-managed security group for " + logicalName),
		VpcId:       pulumi.String(vpcID),
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

const (
	outputKeyHost      = "host"
	outputKeyPort      = "port"
	outputKeyDatabase  = "database"
	outputKeyUsername  = "username"
	outputKeySecretARN = "secretArn"
)
