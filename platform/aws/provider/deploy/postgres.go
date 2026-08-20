package deploy

import (
	ec2 "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	rds "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/rds"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

const (
	maxRDSIdentifierLen       = 63
	rdsAutonameSuffixLen      = 26
	maxRDSIdentifierPrefixLen = maxRDSIdentifierLen - rdsAutonameSuffixLen

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

	Tags map[string]string
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

func rdsIdentifierPrefix(at naming.Coordinate, role string) string {
	ident := at
	ident.Project = naming.SanitizeAlpha(at.Project)
	ident.Name = naming.Join(naming.WordSeparator, at.Name, role)
	return ident.PhysicalPrefix(maxRDSIdentifierPrefixLen)
}

func registerPostgres(ctx *pulumi.Context, project, env, logicalName string, args postgresArgs, vpcID, vpcCIDR string, subnetIDs []string) error {
	at := resourceCoordinate(project, env, logicalName, naming.KindDatabase)

	sg, err := ec2.NewSecurityGroup(ctx, naming.ResourceID(at.Kind, at.Name, "security-group"), &ec2.SecurityGroupArgs{
		Description: pulumi.String(at.Description("security group for the " + at.Name + " database")),
		VpcId:       pulumi.String(vpcID),
		Ingress: ec2.SecurityGroupIngressArray{
			&ec2.SecurityGroupIngressArgs{
				Protocol:    pulumi.String("tcp"),
				FromPort:    pulumi.Int(args.Port),
				ToPort:      pulumi.Int(args.Port),
				CidrBlocks:  pulumi.StringArray{pulumi.String(vpcCIDR)},
				Description: pulumi.String(at.Description("Postgres access to the " + at.Name + " database from within the VPC")),
			},
		},
		Egress: ec2.SecurityGroupEgressArray{
			&ec2.SecurityGroupEgressArgs{
				Protocol:    pulumi.String("-1"),
				FromPort:    pulumi.Int(0),
				ToPort:      pulumi.Int(0),
				CidrBlocks:  pulumi.StringArray{pulumi.String("0.0.0.0/0")},
				Description: pulumi.String(at.Description("outbound access for the " + at.Name + " database")),
			},
		},
		Tags: resourceTags(at.Kind, "", args.Tags),
	})
	if err != nil {
		return err
	}

	subnetGroup, err := rds.NewSubnetGroup(ctx, naming.ResourceID(at.Kind, at.Name, "subnet-group"), &rds.SubnetGroupArgs{
		NamePrefix:  pulumi.String(rdsIdentifierPrefix(at, "subnets")),
		Description: pulumi.String(at.Description("subnet group placing the " + at.Name + " database in the VPC's subnets")),
		SubnetIds:   pulumi.ToStringArray(subnetIDs),
		Tags:        resourceTags(at.Kind, "", args.Tags),
	})
	if err != nil {
		return err
	}

	cluster, err := rds.NewCluster(ctx, naming.ResourceID(at.Kind, at.Name), &rds.ClusterArgs{
		ClusterIdentifierPrefix:  pulumi.String(rdsIdentifierPrefix(at, "")),
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
		Tags: resourceTags(at.Kind, "", args.Tags),
	})
	if err != nil {
		return err
	}

	_, err = rds.NewClusterInstance(ctx, naming.ResourceID(at.Kind, at.Name, "instance"), &rds.ClusterInstanceArgs{
		IdentifierPrefix:   pulumi.String(rdsIdentifierPrefix(at, "instance")),
		ClusterIdentifier:  cluster.ID(),
		Engine:             rds.EngineType(args.Engine),
		EngineVersion:      cluster.EngineVersion,
		InstanceClass:      pulumi.String(args.InstanceClass),
		PubliclyAccessible: pulumi.Bool(args.PubliclyAccessible),
		Tags:               resourceTags(at.Kind, "", args.Tags),
	})
	if err != nil {
		return err
	}

	secretARN := cluster.MasterUserSecrets.Index(pulumi.Int(0)).SecretArn()
	ctx.Export(logicalName, pulumi.Map{
		outputKeyHost:      cluster.Endpoint,
		outputKeyPort:      cluster.Port,
		outputKeyDatabase:  pulumi.String(args.DatabaseName),
		outputKeyUsername:  cluster.MasterUsername,
		outputKeySecretARN: secretARN,
	})

	return nil
}

const (
	outputKeyHost      = "host"
	outputKeyPort      = "port"
	outputKeyDatabase  = "database"
	outputKeyUsername  = "username"
	outputKeySecretARN = "secretArn"
)
