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
    const { publish } = await import("@ocel/sst");

    const vpc = new sst.aws.Vpc("Vpc");
    const orders = new sst.aws.Postgres("Orders", { vpc });
    const account = aws.getCallerIdentityOutput().accountId;

    publish("OcelLinks", {
      project: "with-sst",
      class: "production",
      region,
      links: {
        orders: {
          urn: orders.urn,
          properties: {
            connectionString: $interpolate`postgresql://${orders.username}:${orders.password}@${orders.host}:${orders.port}/${orders.database}`,
            host: orders.host,
            port: orders.port,
            database: orders.database,
          },
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
      },
    });

    return { host: orders.host, database: orders.database };
  },
});
