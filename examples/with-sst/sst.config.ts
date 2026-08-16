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
    const vpc = new sst.aws.Vpc("Vpc");
    const routeTableIds = vpc.nodes.privateRouteTables.apply((tables) => tables.map((t) => t.id));
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

    link.postgres("orders", orders);
    link.custom("network", {
      properties: {
        subnetIds: vpc.privateSubnets,
        securityGroupIds: vpc.securityGroups,
      },
    });

    return { host: orders.host, database: orders.database, port: orders.port };
  },
});
