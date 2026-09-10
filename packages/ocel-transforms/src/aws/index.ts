import type { cloudwatch, ec2, iam, lambda, rds, s3 } from "@pulumi/aws";
import type { Unwrap } from "@pulumi/pulumi";
import type { Patch } from "../patch";

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
} as const satisfies OwnedFieldNames;

type AwsOwned = typeof awsOwnedFields;

type OwnedNames<L> = L extends readonly (infer F)[] ? Extract<F, string> : never;

/**
 * What a rule may patch under `aws`, typed from the args pulumi-aws takes with
 * the fields ocel owns removed. A binding output stands in for any leaf.
 */
export type AwsSurfaces = {
  [T in AwsResourceType]: {
    [K in keyof AwsResourceArgs[T]]: Patch<
      Omit<Unwrap<AwsResourceArgs[T][K]>, OwnedNames<AwsOwned[T][K & keyof AwsOwned[T]]>>
    >;
  };
};
