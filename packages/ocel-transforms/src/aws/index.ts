import type { cloudwatch, ec2, ecs, elasticache, iam, lambda, lb, rds, s3 } from "@pulumi/aws";
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
    /**
     * The role the app's code runs as. Its `managedPolicyArns` attach to it
     * beside the policies ocel attaches, never in their place. Each is a
     * customer-managed policy of the account the app deploys to
     * (`arn:aws:iam::<account>:policy/…`) or one of the AWS service-role
     * policies `AWSLambdaBasicExecutionRole`, `AWSLambdaVPCAccessExecutionRole`
     * and `AmazonECSTaskExecutionRolePolicy`; the deploy refuses any other.
     */
    role: iam.RoleArgs;
  };
  /**
   * A container app: its Fargate task and service, the target group and
   * listener rule that route the app's requests to it, and the task role, which
   * exists once the app reaches a binding, a task or a live variable, or a rule
   * attaches a managed policy to `role`.
   */
  container: {
    task: ecs.TaskDefinitionArgs;
    service: ecs.ServiceArgs;
    targetGroup: lb.TargetGroupArgs;
    rule: lb.ListenerRuleArgs;
    /**
     * The role the app's code runs as. Its `managedPolicyArns` attach to it
     * beside the policies ocel attaches, never in their place. Each is a
     * customer-managed policy of the account the app deploys to
     * (`arn:aws:iam::<account>:policy/…`) or one of the AWS service-role
     * policies `AWSLambdaBasicExecutionRole`, `AWSLambdaVPCAccessExecutionRole`
     * and `AmazonECSTaskExecutionRolePolicy`; the deploy refuses any other.
     */
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
    /**
     * The tier's VPC. Its `cidrBlock` is the block ocel carves the six subnets
     * from: a `/16` to a `/24` inside 10.0.0.0/8, 172.16.0.0/12 or
     * 192.168.0.0/16. Left off, the tier takes the first `/16` from 10.16.0.0
     * to 10.255.0.0 that no VPC in the region overlaps. It is fixed once the
     * VPC exists: a deploy that asks for another block is refused.
     */
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
 * where it is written.
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
      "name",
      "namePrefix",
      "permissionsBoundary",
      "tags",
    ],
  },
  container: {
    task: [
      "containerDefinitions",
      "executionRoleArn",
      "family",
      "networkMode",
      "requiresCompatibilities",
      "runtimePlatform",
      "tags",
      "taskRoleArn",
    ],
    service: [
      "cluster",
      "desiredCount",
      "launchType",
      "loadBalancers",
      "name",
      "networkConfiguration",
      "tags",
      "taskDefinition",
      "waitForSteadyState",
    ],
    targetGroup: ["name", "namePrefix", "port", "protocol", "tags", "targetType", "vpcId"],
    rule: ["actions", "conditions", "listenerArn", "priority", "tags"],
    role: [
      "assumeRolePolicy",
      "inlinePolicies",
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
    vpc: [
      "enableDnsHostnames",
      "enableDnsSupport",
      "instanceTenancy",
      "ipv4IpamPoolId",
      "ipv4NetmaskLength",
      "tags",
    ],
    internetGateway: ["tags", "vpcId"],
    publicSubnet: [
      "availabilityZone",
      "availabilityZoneId",
      "cidrBlock",
      "ipv6CidrBlock",
      "ipv6Native",
      "mapPublicIpOnLaunch",
      "outpostArn",
      "tags",
      "vpcId",
    ],
    privateSubnet: [
      "availabilityZone",
      "availabilityZoneId",
      "cidrBlock",
      "ipv6CidrBlock",
      "ipv6Native",
      "mapPublicIpOnLaunch",
      "outpostArn",
      "tags",
      "vpcId",
    ],
    isolatedSubnet: [
      "availabilityZone",
      "availabilityZoneId",
      "cidrBlock",
      "ipv6CidrBlock",
      "ipv6Native",
      "mapPublicIpOnLaunch",
      "outpostArn",
      "tags",
      "vpcId",
    ],
    publicRouteTable: ["routes", "tags", "vpcId"],
    privateRouteTable: ["routes", "tags", "vpcId"],
    isolatedRouteTable: ["routes", "tags", "vpcId"],
    s3Endpoint: ["routeTableIds", "serviceName", "tags", "vpcEndpointType", "vpcId"],
    dynamodbEndpoint: ["routeTableIds", "serviceName", "tags", "vpcEndpointType", "vpcId"],
    natGateway: [
      "allocationId",
      "connectivityType",
      "privateIp",
      "secondaryAllocationIds",
      "secondaryPrivateIpAddresses",
      "subnetId",
      "tags",
    ],
    natAddress: [
      "address",
      "associateWithPrivateIp",
      "domain",
      "instance",
      "networkInterface",
      "tags",
    ],
  },
} as const satisfies OwnedFieldNames;

type AwsOwned = typeof awsOwnedFields;

type OwnedNames<L> = L extends readonly (infer F)[] ? Extract<F, string> : never;

type WrittenDown<T> = T extends undefined | null
  ? never
  : T extends readonly (infer E)[]
    ? WrittenDown<E>[]
    : T extends object
      ? { readonly [K in keyof T]?: WrittenDown<T[K]> }
      : T;

type Patchable<T extends AwsResourceType, K extends keyof AwsResourceArgs[T]> = Omit<
  Unwrap<AwsResourceArgs[T][K]>,
  OwnedNames<AwsOwned[T][K & keyof AwsOwned[T]]>
>;

/**
 * What a rule may patch under `aws`, typed from the args pulumi-aws takes with
 * the fields ocel owns removed. A binding output can take the place of any leaf
 * but the network's: the network belongs to every project of the tier, so no
 * project's binding fills it.
 */
export type AwsSurfaces = {
  [T in AwsResourceType]: {
    [K in keyof AwsResourceArgs[T]]: T extends "network"
      ? WrittenDown<Patchable<T, K>>
      : Patch<Patchable<T, K>>;
  };
};
