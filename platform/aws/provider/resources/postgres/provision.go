package postgres

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	ec2 "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	rds "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/rds"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

const (
	maxRDSIdentifierLen = 63

	instanceSuffix    = "1"
	subnetsSuffix     = "subnets"
	finalSnapshotWord = "final"
	appRolePart       = "app-role"

	postgresEngine               = "aurora-postgresql"
	postgresEngineMode           = "provisioned"
	defaultPostgresEngineVersion = "16.4"
	postgresInstanceClass        = "db.serverless"
	Port                         = 5432
	postgresMasterUsername       = "ocel"
	postgresDatabaseName         = "ocel"
	serverCA                     = "rds-ca-rsa2048-g1"
	serverCARoot                 = "RSA2048 G1"

	finalSnapshotStampChars = 7
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

	Tier environment.Tier
	Tags map[string]string
}

type capacity struct {
	min, max float64
}

func capacityOf(tier environment.Tier) capacity {
	if tier == environment.TierProduction {
		return capacity{min: 0.5, max: 8}
	}
	return capacity{min: 0, max: 2}
}

func translate(spec *provider.PostgresSpec, tier environment.Tier) clusterArgs {
	version := defaultPostgresEngineVersion
	if spec != nil && spec.Version != "" {
		version = spec.Version
	}
	return clusterArgs{
		Engine:               postgresEngine,
		EngineMode:           postgresEngineMode,
		EngineVersion:        version,
		MinCapacity:          capacityOf(tier).min,
		MaxCapacity:          capacityOf(tier).max,
		InstanceClass:        postgresInstanceClass,
		Port:                 Port,
		MasterUsername:       postgresMasterUsername,
		DatabaseName:         postgresDatabaseName,
		ManageMasterPassword: true,
		PubliclyAccessible:   false,
		Tier:                 tier,
	}
}

func finalSnapshotIdentifier(cluster string, protected bool, now time.Time) pulumi.StringPtrInput {
	if !protected {
		return nil
	}
	return pulumi.String(finalSnapshotName(cluster, now))
}

func finalSnapshotName(cluster string, now time.Time) string {
	return cluster + naming.WordSeparator + finalSnapshotSuffix(now)
}

func finalSnapshotSuffix(now time.Time) string {
	stamp := strconv.FormatInt(now.Unix(), 36)
	return finalSnapshotWord + naming.WordSeparator + strings.Repeat("0", max(0, finalSnapshotStampChars-len(stamp))) + stamp
}

func ClusterName(namespace string, tier environment.Tier, at naming.Coordinate) (string, error) {
	name := at.AppResourceName(namespace, at.HashTag(namespace))
	name.Qualifier = naming.TierWord(tier)
	return name.Fit(maxRDSIdentifierLen, instanceSuffix, subnetsSuffix, component.SecurityGroupSuffix, finalSnapshotSuffix(time.Unix(0, 0)))
}

func register(ctx *pulumi.Context, stack component.Stack, logicalName string, args clusterArgs) error {
	at := component.ResourceCoordinate(stack.Project, stack.Env, logicalName, naming.KindDatabase)
	network := stack.Network
	names, err := NamesFor(stack, logicalName)
	if err != nil {
		return err
	}
	name := names.Cluster

	securityGroup, err := component.NewVPCSecurityGroup(ctx, at, component.VPCSecurityGroup{
		Name:    names.SecurityGroup,
		Subject: "the " + at.Name + " database",
		Engine:  "Postgres",
		Port:    args.Port,
		VPCID:   network.VPCID,
		Clients: network.Clients,
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

	subnetGroup, err := rds.NewSubnetGroup(ctx, names.Subnets, &rds.SubnetGroupArgs{
		Name:        pulumi.String(names.Subnets),
		Description: component.CapDescription(at.Description("subnet group placing the "+at.Name+" database in the VPC's subnets"), component.MaxSubnetGroupDescriptionLen),
		SubnetIds:   pulumi.ToStringArray(network.SubnetIDs),
		Tags:        component.Tags(at.Kind, "", args.Tags),
	})
	if err != nil {
		return err
	}

	cluster, err := rds.NewCluster(ctx, name, &rds.ClusterArgs{
		ClusterIdentifier:                pulumi.String(name),
		Engine:                           pulumi.String(args.Engine),
		EngineMode:                       pulumi.String(args.EngineMode),
		EngineVersion:                    pulumi.String(args.EngineVersion),
		DatabaseName:                     pulumi.String(args.DatabaseName),
		MasterUsername:                   pulumi.String(args.MasterUsername),
		ManageMasterUserPassword:         pulumi.Bool(args.ManageMasterPassword),
		IamDatabaseAuthenticationEnabled: pulumi.Bool(true),
		EnableHttpEndpoint:               pulumi.Bool(true),
		DbSubnetGroupName:                subnetGroup.Name,
		VpcSecurityGroupIds:              pulumi.StringArray{securityGroup.ID()},
		DeletionProtection:               pulumi.Bool(args.Protected),
		SkipFinalSnapshot:                pulumi.Bool(!args.Protected),
		FinalSnapshotIdentifier:          finalSnapshotIdentifier(name, args.Protected, time.Now()),
		Serverlessv2ScalingConfiguration: &rds.ClusterServerlessv2ScalingConfigurationArgs{
			MinCapacity: pulumi.Float64(args.MinCapacity),
			MaxCapacity: pulumi.Float64(args.MaxCapacity),
		},
		Tags: component.Tags(at.Kind, "", args.Tags),
	}, pulumi.IgnoreChanges([]string{"finalSnapshotIdentifier"}))
	if err != nil {
		return err
	}

	instance, err := rds.NewClusterInstance(ctx, names.Instance, &rds.ClusterInstanceArgs{
		Identifier:         pulumi.String(names.Instance),
		ClusterIdentifier:  cluster.ID(),
		Engine:             rds.EngineType(args.Engine),
		EngineVersion:      cluster.EngineVersion,
		InstanceClass:      pulumi.String(args.InstanceClass),
		CaCertIdentifier:   pulumi.String(serverCA),
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
	if err := ctx.RegisterResource(RoleToken, name+component.PartSeparator+appRolePart, pulumi.Map{
		"cluster":  cluster.Arn,
		"secret":   secretARN,
		"database": pulumi.String(args.DatabaseName),
		"master":   cluster.MasterUsername,
		"role":     pulumi.String(AppRoleFor(args.Tier)),
	}, &appRoleState{}, pulumi.DependsOn([]pulumi.Resource{instance})); err != nil {
		return err
	}
	ctx.Export(logicalName, pulumi.Map{
		component.OutputKeyHost: cluster.Endpoint,
		component.OutputKeyPort: cluster.Port,
		OutputKeyDatabase:       pulumi.String(args.DatabaseName),
		OutputKeyUsername:       pulumi.String(AppRoleFor(args.Tier)),
		OutputKeyClusterARN:     cluster.Arn,
		OutputKeyResourceID:     cluster.ClusterResourceId,

		OutputKeyClusterIdentifier: cluster.ID(),
	})

	return nil
}

const (
	OutputKeyDatabase   = "database"
	OutputKeyUsername   = "username"
	OutputKeyClusterARN = "clusterArn"
	OutputKeyResourceID = "resourceId"

	OutputKeyClusterIdentifier = "clusterIdentifier"
)
