/// <reference path="./.sst/platform/config.d.ts" />

const region = "us-east-1";

export default $config({
  app() {
    return {
      name: "with-sst",
      home: "aws",
      providers: { aws: { region } },
    };
  },
  async run() {
    const { link } = await import("@ocel/sst");
    const environment = process.env.OCEL_LINK_ENVIRONMENT;
    const target = environment
      ? {
          project: process.env.OCEL_LINK_PROJECT,
          class: "preview" as const,
          environment,
        }
      : undefined;
    const vpc = new sst.aws.Vpc("Vpc");
    const routeTableIds = vpc.nodes.privateRouteTables.apply((tables) =>
      tables.map((table) => table.id),
    );
    for (const [name, service] of [
      ["S3", "s3"],
      ["Dynamo", "dynamodb"],
    ]) {
      new aws.ec2.VpcEndpoint(name, {
        vpcId: vpc.id,
        serviceName: `com.amazonaws.${region}.${service}`,
        vpcEndpointType: "Gateway",
        routeTableIds,
      });
    }
    new aws.ec2.VpcEndpoint("Kms", {
      vpcId: vpc.id,
      serviceName: `com.amazonaws.${region}.kms`,
      vpcEndpointType: "Interface",
      privateDnsEnabled: true,
      subnetIds: vpc.privateSubnets,
      securityGroupIds: vpc.securityGroups,
    });

    const orders = new sst.aws.Postgres("Orders", { vpc });
    const account = aws.getCallerIdentityOutput().accountId;

    link.postgres(
      "orders",
      {
        host: orders.host,
        port: orders.port,
        database: orders.database,
        username: orders.username,
        password: orders.password,
        grants: [
          {
            label: "connect",
            actions: ["rds-db:connect"],
            resources: [
              $interpolate`arn:aws:rds-db:${region}:${account}:dbuser:${orders.nodes.cluster.clusterResourceId}/${orders.username}`,
            ],
          },
        ],
      },
      target,
    );
    link.custom(
      "network",
      {
        properties: {
          subnetIds: vpc.privateSubnets,
          securityGroupIds: vpc.securityGroups,
        },
      },
      target,
    );

    return {
      host: orders.host,
      database: orders.database,
      port: orders.port,
      subnetIds: vpc.privateSubnets.apply((ids) => ids.join(",")),
      securityGroupIds: vpc.securityGroups.apply((ids) => ids.join(",")),
    };
  },
});
