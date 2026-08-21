import * as aws from "@pulumi/aws";
import { Config, interpolate } from "@pulumi/pulumi";
import { link } from "@ocel/pulumi";

const region = aws.config.requireRegion();
const password = new Config().requireSecret("dbPassword");
const environment = process.env.OCEL_LINK_ENVIRONMENT;
const target = environment
  ? {
      project: process.env.OCEL_LINK_PROJECT,
      class: "preview" as const,
      environment,
    }
  : undefined;

const vpc = new aws.ec2.Vpc("orders", {
  cidrBlock: "10.0.0.0/16",
  enableDnsSupport: true,
  enableDnsHostnames: true,
});

const routes = new aws.ec2.RouteTable("orders", { vpcId: vpc.id });

const zones = aws.getAvailabilityZonesOutput({ state: "available" });

const subnets = [0, 1].map((index) => {
  const subnet = new aws.ec2.Subnet(`orders-${index}`, {
    vpcId: vpc.id,
    cidrBlock: `10.0.${index}.0/24`,
    availabilityZone: zones.names.apply((names) => names[index]),
  });
  new aws.ec2.RouteTableAssociation(`orders-${index}`, {
    subnetId: subnet.id,
    routeTableId: routes.id,
  });
  return subnet;
});

const subnetIds = subnets.map((subnet) => subnet.id);

const security = new aws.ec2.SecurityGroup("orders", {
  vpcId: vpc.id,
  ingress: [
    { protocol: "tcp", fromPort: 443, toPort: 443, self: true },
    { protocol: "tcp", fromPort: 5432, toPort: 5432, self: true },
  ],
  egress: [
    { protocol: "-1", fromPort: 0, toPort: 0, cidrBlocks: ["0.0.0.0/0"] },
  ],
});

for (const service of ["s3", "dynamodb"]) {
  new aws.ec2.VpcEndpoint(service, {
    vpcId: vpc.id,
    serviceName: `com.amazonaws.${region}.${service}`,
    vpcEndpointType: "Gateway",
    routeTableIds: [routes.id],
  });
}

new aws.ec2.VpcEndpoint("kms", {
  vpcId: vpc.id,
  serviceName: interpolate`com.amazonaws.${region}.kms`,
  vpcEndpointType: "Interface",
  privateDnsEnabled: true,
  subnetIds,
  securityGroupIds: [security.id],
});

const group = new aws.rds.SubnetGroup("orders", { subnetIds });

const orders = new aws.rds.Instance("orders", {
  engine: "postgres",
  engineVersion: "17",
  instanceClass: aws.rds.InstanceType.T4G_Micro,
  allocatedStorage: 20,
  dbName: "orders",
  username: "postgres",
  password,
  dbSubnetGroupName: group.name,
  vpcSecurityGroupIds: [security.id],
  publiclyAccessible: false,
  skipFinalSnapshot: true,
});

const account = aws.getCallerIdentityOutput().accountId;

link.postgres(
  "orders",
  {
    host: orders.address,
    port: orders.port,
    database: orders.dbName,
    username: orders.username,
    password,
    grants: [
      {
        label: "connect",
        actions: ["rds-db:connect"],
        resources: [
          interpolate`arn:aws:rds-db:${region}:${account}:dbuser:${orders.resourceId}/${orders.username}`,
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
      subnetIds,
      securityGroupIds: [security.id],
    },
  },
  target,
);

export const endpoint = interpolate`${orders.address}:${orders.port}`;
export const host = orders.address;
export const port = orders.port;
export const database = orders.dbName;
export const publishedSubnetIds =
  interpolate`${subnets[0]!.id},${subnets[1]!.id}`;
export const publishedSecurityGroupIds = security.id;
