package deploy

import (
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// maxPostgresIdentLen is the Postgres identifier byte limit; a logical-slice
// database name is truncated to fit it.
const maxPostgresIdentLen = 63

// postgresSliceArgs is the fully-resolved input for realizing an ephemeral
// preview postgres resource as a logical database inside the shared preview
// cluster. It is the pure counterpart to postgresArgs for the sliced path.
type postgresSliceArgs struct {
	// DatabaseName is the substrate-safe name of the logical database to carve.
	DatabaseName string
	// ClusterEndpoint is the shared preview cluster's host (from the preview
	// bootstrap outputs); the slice lives inside it rather than a new cluster.
	ClusterEndpoint string
	// AdminSecretARN is the shared cluster's admin master-user secret, used to
	// connect as the owner that runs CREATE/DROP DATABASE.
	AdminSecretARN string
}

// sliceDatabaseName derives the logical database name for an ephemeral preview
// resource: the environment identity and the resource's logical name joined,
// both already substrate-safe, truncated to a valid Postgres identifier. It is
// pure.
func sliceDatabaseName(identity, logicalName string) string {
	name := identity + "_" + logicalName
	if len(name) > maxPostgresIdentLen {
		name = name[:maxPostgresIdentLen]
	}
	return name
}

// registerPostgresLogicalSlice realizes an ephemeral preview postgres resource
// as a logical database carved from the shared preview cluster, and exports the
// resource's connection outputs under logicalName (the same output shape
// registerPostgres exports, so collectPostgresOutput reads either path
// uniformly).
//
// The database itself is created by a Pulumi custom resource whose create runs
// `CREATE DATABASE <args.DatabaseName>` and whose delete runs `DROP DATABASE`
// against args.ClusterEndpoint as the admin user (args.AdminSecretARN). That
// dynamic resource needs the pulumi-postgresql provider and a live shared
// cluster to connect to, so — exactly like deploy.Run's own real-provisioning
// body — it is the opt-in-e2e seam: this signature is final, and the resource
// registration lands here once the preview cluster is real. Until then a
// preview-ephemeral deploy is only exercised by an opt-in run against live
// preview infrastructure.
func registerPostgresLogicalSlice(ctx *pulumi.Context, logicalName string, args postgresSliceArgs) error {
	ctx.Export(logicalName, pulumi.Map{
		outputKeyHost:      pulumi.String(args.ClusterEndpoint),
		outputKeyPort:      pulumi.Int(postgresPort),
		outputKeyDatabase:  pulumi.String(args.DatabaseName),
		outputKeyUsername:  pulumi.String(postgresMasterUsername),
		outputKeySecretARN: pulumi.String(args.AdminSecretARN),
	})
	return nil
}
