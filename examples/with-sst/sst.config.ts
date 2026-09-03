/// <reference path="./.sst/platform/config.d.ts" />

const region = "us-east-1";

const emulator = process.env.AWS_ENDPOINT_URL;

const emulated = emulator
  ? {
      accessKey: process.env.AWS_ACCESS_KEY_ID ?? "test",
      secretKey: process.env.AWS_SECRET_ACCESS_KEY ?? "test",
      skipCredentialsValidation: true,
      skipRequestingAccountId: true,
      skipMetadataApiCheck: true,
      s3UsePathStyle: true,
      endpoints: [
        Object.fromEntries(
          [
            "acm",
            "apigateway",
            "apigatewayv2",
            "cloudformation",
            "cloudfront",
            "cloudwatch",
            "cloudwatchlogs",
            "dynamodb",
            "ec2",
            "ecr",
            "ecs",
            "elbv2",
            "events",
            "iam",
            "kms",
            "lambda",
            "logs",
            "rds",
            "route53",
            "s3",
            "secretsmanager",
            "sns",
            "sqs",
            "ssm",
            "sts",
          ].map((service) => [service, emulator]),
        ),
      ],
    }
  : {};

export default $config({
  app() {
    return {
      name: "with-sst",
      home: "aws",
      providers: { aws: { region, ...emulated } },
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
