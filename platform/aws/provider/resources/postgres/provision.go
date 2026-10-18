package postgres

import (
	"fmt"
	"time"

	ec2 "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	rds "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/rds"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
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
	Port                         = 5432
	postgresMasterUsername       = "ocel"
	postgresDatabaseName         = "ocel"

	finalSnapshotTimeLayout = "20060102150405"
)

type clusterArgs struct {
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
	Protected            bool

	Tags map[string]string
}

func translate(spec *provider.PostgresSpec) clusterArgs {
	version := defaultPostgresEngineVersion
	if spec != nil && spec.Version != "" {
		version = spec.Version
	}
	return clusterArgs{
		Engine:               postgresEngine,
		EngineMode:           postgresEngineMode,
		EngineVersion:        version,
		MinCapacity:          postgresMinCapacity,
		MaxCapacity:          postgresMaxCapacity,
		InstanceClass:        postgresInstanceClass,
		Port:                 Port,
		MasterUsername:       postgresMasterUsername,
		DatabaseName:         postgresDatabaseName,
		ManageMasterPassword: true,
		PubliclyAccessible:   false,
	}
}

func finalSnapshotIdentifier(at naming.Coordinate, protected bool, now time.Time) pulumi.StringPtrInput {
	if !protected {
		return nil
	}
	return pulumi.String(rdsIdentifierPrefix(at, "final") + now.UTC().Format(finalSnapshotTimeLayout))
}

func rdsIdentifierPrefix(at naming.Coordinate, role string) string {
	ident := at
	ident.Project = naming.SanitizeAlpha(at.Project)
	ident.Name = naming.Join(naming.WordSeparator, at.Name, role)
	return component.AppPhysicalPrefix(ident, maxRDSIdentifierPrefixLen)
}

func register(ctx *pulumi.Context, project, env, logicalName string, args clusterArgs, network component.Network) error {
	at := component.ResourceCoordinate(project, env, logicalName, naming.KindDatabase)

	securityGroup, err := component.NewVPCSecurityGroup(ctx, at, component.VPCSecurityGroup{
		Subject: "the " + at.Name + " database",
		Engine:  "Postgres",
		Port:    args.Port,
		VPCID:   network.VPCID,
		VPCCIDR: network.VPCCIDR,
		Egress: ec2.SecurityGroupEgressArray{
			&ec2.SecurityGroupEgressArgs{
				Protocol:    pulumi.String("-1"),
				FromPort:    pulumi.Int(0),
				ToPort:      pulumi.Int(0),
				CidrBlocks:  pulumi.StringArray{pulumi.String("0.0.0.0/0")},
				Description: component.CapDescription(at.Description("outbound access for the "+at.Name+" database"), component.MaxSecurityGroupDescriptionLen),
			},
		},
		Tags: component.Tags(at.Kind, "", args.Tags),
	})
	if err != nil {
		return err
	}

	subnetGroup, err := rds.NewSubnetGroup(ctx, naming.ResourceID(at.Kind, at.Name, "subnet-group"), &rds.SubnetGroupArgs{
		NamePrefix:  pulumi.String(rdsIdentifierPrefix(at, "subnets")),
		Description: component.CapDescription(at.Description("subnet group placing the "+at.Name+" database in the VPC's subnets"), component.MaxSubnetGroupDescriptionLen),
		SubnetIds:   pulumi.ToStringArray(network.SubnetIDs),
		Tags:        component.Tags(at.Kind, "", args.Tags),
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
		VpcSecurityGroupIds:      pulumi.StringArray{securityGroup.ID()},
		DeletionProtection:       pulumi.Bool(args.Protected),
		SkipFinalSnapshot:        pulumi.Bool(!args.Protected),
		FinalSnapshotIdentifier:  finalSnapshotIdentifier(at, args.Protected, time.Now()),
		Serverlessv2ScalingConfiguration: &rds.ClusterServerlessv2ScalingConfigurationArgs{
			MinCapacity: pulumi.Float64(args.MinCapacity),
			MaxCapacity: pulumi.Float64(args.MaxCapacity),
		},
		Tags: component.Tags(at.Kind, "", args.Tags),
	}, pulumi.IgnoreChanges([]string{"finalSnapshotIdentifier"}))
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
		Tags:               component.Tags(at.Kind, "", args.Tags),
	})
	if err != nil {
		return err
	}

	secretARN := cluster.MasterUserSecrets.ApplyT(func(secrets []rds.ClusterMasterUserSecret) (string, error) {
		if len(secrets) == 0 || secrets[0].SecretArn == nil {
			return "", fmt.Errorf("the %s cluster reports no managed master user secret, so nothing can read the password %s needs", at.Name, args.DatabaseName)
		}
		return *secrets[0].SecretArn, nil
	}).(pulumi.StringOutput)
	ctx.Export(logicalName, pulumi.Map{
		component.OutputKeyHost: cluster.Endpoint,
		component.OutputKeyPort: cluster.Port,
		OutputKeyDatabase:       pulumi.String(args.DatabaseName),
		OutputKeyUsername:       cluster.MasterUsername,
		OutputKeySecretARN:      secretARN,

		OutputKeyClusterIdentifier: cluster.ID(),
	})

	return nil
}

const (
	OutputKeyDatabase  = "database"
	OutputKeyUsername  = "username"
	OutputKeySecretARN = "secretArn"

	OutputKeyClusterIdentifier = "clusterIdentifier"
)
