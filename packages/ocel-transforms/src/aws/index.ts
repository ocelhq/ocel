import type { cloudwatch, ec2, elasticache, iam, lambda, rds, s3 } from "@pulumi/aws";
import type { Unwrap } from "@pulumi/pulumi";
import type { Patch } from "../patch.js";

/**
 * One key per Pulumi resource the AWS provider constructs for an ocel resource,
 * named after the pulumi-aws module it comes from.
 */
export interface AwsResourceArgs {
  function: {
    lambda: lambda.FunctionArgs;
    url: lambda.FunctionUrlArgs;
    urlPermission: lambda.PermissionArgs;
    logGroup: cloudwatch.LogGroupArgs;
    role: iam.RoleArgs;
  };
  bucket: {
    bucket: s3.BucketV2Args;
    publicAccessBlock: s3.BucketPublicAccessBlockArgs;
    cors: s3.BucketCorsConfigurationV2Args;
    uploadCompleterRole: iam.RoleArgs;
    uploadCompleterS3Policy: iam.RolePolicyArgs;
    uploadCompleterSessionsPolicy: iam.RolePolicyArgs;
    uploadCompleterLogsPolicy: iam.RolePolicyAttachmentArgs;
    uploadCompleterLogGroup: cloudwatch.LogGroupArgs;
    uploadCompleter: lambda.FunctionArgs;
    uploadCompleterPermission: lambda.PermissionArgs;
    notification: s3.BucketNotificationArgs;
  };
  postgres: {
    securityGroup: ec2.SecurityGroupArgs;
    subnetGroup: rds.SubnetGroupArgs;
    cluster: rds.ClusterArgs;
    instance: rds.ClusterInstanceArgs;
  };
  /**
   * A kv store: a Valkey replication group of two nodes across zones, each the
   * cheapest node that holds the store's declared memory. Setting
   * `numCacheClusters: 1` with `automaticFailoverEnabled` and `multiAzEnabled`
   * off halves its cost, and a store whose one node is replaced comes back empty.
   * A deploy refuses one node with either flag left on.
   */
  kv: {
    securityGroup: ec2.SecurityGroupArgs;
    subnetGroup: elasticache.SubnetGroupArgs;
    parameterGroup: elasticache.ParameterGroupArgs;
    replicationGroup: elasticache.ReplicationGroupArgs;
  };
  /**
   * The VPC of the tier, shared by every project deployed to it: one patch
   * reaches every zone's subnet or route table of the kind it names, and the
   * NAT gateways and their addresses exist once a serverless app reaches a
   * database or cache. A rule sees no `env` or `app` for it. Patches from the
   * projects of one tier must agree: a deploy whose patches differ from the
   * ones the network carries provisions it again with its own.
   */
  network: {
    vpc: ec2.VpcArgs;
    internetGateway: ec2.InternetGatewayArgs;
    publicSubnet: ec2.SubnetArgs;
    privateSubnet: ec2.SubnetArgs;
    isolatedSubnet: ec2.SubnetArgs;
    publicRouteTable: ec2.RouteTableArgs;
    privateRouteTable: ec2.RouteTableArgs;
    isolatedRouteTable: ec2.RouteTableArgs;
    s3Endpoint: ec2.VpcEndpointArgs;
    dynamodbEndpoint: ec2.VpcEndpointArgs;
    natGateway: ec2.NatGatewayArgs;
    natAddress: ec2.EipArgs;
  };
}

/** The ocel resource types the AWS provider renders patchable resources for. */
export type AwsResourceType = keyof AwsResourceArgs;

type OwnedFieldNames = {
  [T in AwsResourceType]: {
    [K in keyof AwsResourceArgs[T]]: readonly (keyof Unwrap<AwsResourceArgs[T][K]>)[];
  };
};

/**
 * The fields ocel fills from its own state — the names it minted, the ARNs it
 * created, the code it uploaded and the runtime that code was built for, the
 * environment it sealed, the tags it sweeps by. A patch naming one is refused
 * where it is written, and the deploy refuses it again by name.
 */
export const awsOwnedFields = {
  function: {
    lambda: [
      "architectures",
      "code",
      "environment",
      "handler",
      "imageUri",
      "layers",
      "loggingConfig",
      "name",
      "packageType",
      "role",
      "runtime",
      "s3Bucket",
      "s3Key",
      "s3ObjectVersion",
      "sourceCodeHash",
      "tags",
    ],
    url: ["authorizationType", "functionName", "qualifier"],
    urlPermission: ["action", "function", "functionUrlAuthType", "principal", "qualifier"],
    logGroup: ["name", "namePrefix", "tags"],
    role: [
      "assumeRolePolicy",
      "inlinePolicies",
      "managedPolicyArns",
      "name",
      "namePrefix",
      "permissionsBoundary",
      "tags",
    ],
  },
  bucket: {
    bucket: ["bucket", "bucketPrefix", "tags"],
    publicAccessBlock: ["bucket"],
    cors: ["bucket"],
    uploadCompleterRole: [
      "assumeRolePolicy",
      "inlinePolicies",
      "managedPolicyArns",
      "name",
      "namePrefix",
      "permissionsBoundary",
      "tags",
    ],
    uploadCompleterS3Policy: ["name", "namePrefix", "policy", "role"],
    uploadCompleterSessionsPolicy: ["name", "namePrefix", "policy", "role"],
    uploadCompleterLogsPolicy: ["policyArn", "role"],
    uploadCompleterLogGroup: ["name", "namePrefix", "tags"],
    uploadCompleter: [
      "architectures",
      "code",
      "environment",
      "handler",
      "imageUri",
      "loggingConfig",
      "name",
      "packageType",
      "role",
      "runtime",
      "s3Bucket",
      "s3Key",
      "s3ObjectVersion",
      "sourceCodeHash",
      "tags",
    ],
    uploadCompleterPermission: ["action", "function", "principal", "qualifier", "sourceArn"],
    notification: ["bucket", "lambdaFunctions"],
  },
  postgres: {
    securityGroup: ["name", "namePrefix", "tags", "vpcId"],
    subnetGroup: ["name", "namePrefix", "subnetIds", "tags"],
    cluster: [
      "clusterIdentifier",
      "clusterIdentifierPrefix",
      "databaseName",
      "dbSubnetGroupName",
      "engine",
      "manageMasterUserPassword",
      "masterPassword",
      "masterUsername",
      "tags",
      "vpcSecurityGroupIds",
    ],
    instance: [
      "clusterIdentifier",
      "engine",
      "engineVersion",
      "identifier",
      "identifierPrefix",
      "tags",
    ],
  },
  kv: {
    securityGroup: ["name", "namePrefix", "tags", "vpcId"],
    subnetGroup: ["name", "subnetIds", "tags"],
    parameterGroup: ["family", "name", "parameters", "tags"],
    replicationGroup: [
      "authToken",
      "authTokenUpdateStrategy",
      "clusterMode",
      "engine",
      "engineVersion",
      "globalReplicationGroupId",
      "nodeType",
      "numNodeGroups",
      "parameterGroupName",
      "port",
      "replicasPerNodeGroup",
      "replicationGroupId",
      "securityGroupIds",
      "securityGroupNames",
      "subnetGroupName",
      "tags",
      "transitEncryptionEnabled",
      "transitEncryptionMode",
      "userGroupIds",
    ],
  },
  network: {
    vpc: ["cidrBlock", "ipv4IpamPoolId", "ipv4NetmaskLength", "tags"],
    internetGateway: ["tags", "vpcId"],
    publicSubnet: [
      "availabilityZone",
      "availabilityZoneId",
      "cidrBlock",
      "mapPublicIpOnLaunch",
      "tags",
      "vpcId",
    ],
    privateSubnet: [
      "availabilityZone",
      "availabilityZoneId",
      "cidrBlock",
      "mapPublicIpOnLaunch",
      "tags",
      "vpcId",
    ],
    isolatedSubnet: [
      "availabilityZone",
      "availabilityZoneId",
      "cidrBlock",
      "mapPublicIpOnLaunch",
      "tags",
      "vpcId",
    ],
    publicRouteTable: ["routes", "tags", "vpcId"],
    privateRouteTable: ["routes", "tags", "vpcId"],
    isolatedRouteTable: ["routes", "tags", "vpcId"],
    s3Endpoint: ["routeTableIds", "serviceName", "tags", "vpcEndpointType", "vpcId"],
    dynamodbEndpoint: ["routeTableIds", "serviceName", "tags", "vpcEndpointType", "vpcId"],
    natGateway: ["allocationId", "subnetId", "tags"],
    natAddress: ["domain", "tags"],
  },
} as const satisfies OwnedFieldNames;

type AwsOwned = typeof awsOwnedFields;

type OwnedNames<L> = L extends readonly (infer F)[] ? Extract<F, string> : never;

/**
 * What a rule may patch under `aws`, typed from the args pulumi-aws takes with
 * the fields ocel owns removed. A binding output can take the place of any leaf.
 */
export type AwsSurfaces = {
  [T in AwsResourceType]: {
    [K in keyof AwsResourceArgs[T]]: Patch<
      Omit<Unwrap<AwsResourceArgs[T][K]>, OwnedNames<AwsOwned[T][K & keyof AwsOwned[T]]>>
    >;
  };
};
