package deploy

import (
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const maxPostgresIdentLen = 63

type postgresSliceArgs struct {
	DatabaseName    string
	ClusterEndpoint string
	AdminSecretARN  string
}

func sliceDatabaseName(identity, logicalName string) string {
	name := identity + "_" + logicalName
	if len(name) > maxPostgresIdentLen {
		name = name[:maxPostgresIdentLen]
	}
	return name
}

func registerPostgresLogicalSlice(ctx *pulumi.Context, logicalName string, args postgresSliceArgs) (pulumi.StringOutput, error) {
	ctx.Export(logicalName, pulumi.Map{
		outputKeyHost:      pulumi.String(args.ClusterEndpoint),
		outputKeyPort:      pulumi.Int(postgresPort),
		outputKeyDatabase:  pulumi.String(args.DatabaseName),
		outputKeyUsername:  pulumi.String(postgresMasterUsername),
		outputKeySecretARN: pulumi.String(args.AdminSecretARN),
	})

	return postgresEnvValue(ctx, pulumi.String(postgresMasterUsername), pulumi.String(args.ClusterEndpoint), pulumi.Int(postgresPort), args.DatabaseName, pulumi.String(args.AdminSecretARN)), nil
}
