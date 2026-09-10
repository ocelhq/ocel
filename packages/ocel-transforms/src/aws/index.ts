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
    logGroup: cloudwatch.LogGroupArgs;
  };
  bucket: {
    bucket: s3.BucketV2Args;
    publicAccessBlock: s3.BucketPublicAccessBlockArgs;
    cors: s3.BucketCorsConfigurationV2Args;
    uploadCompleterRole: iam.RoleArgs;
    uploadCompleterLogGroup: cloudwatch.LogGroupArgs;
    uploadCompleter: lambda.FunctionArgs;
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
 * created, the code it uploaded, the environment it sealed. A patch naming one
 * is refused where it is written, and the deploy refuses it again by name.
 */
export const awsOwnedFields = {
  function: {
    lambda: [
      "handler",
      "role",
      "s3Bucket",
      "s3Key",
      "environment",
      "loggingConfig",
      "layers",
      "architectures",
    ],
    url: ["functionName", "authorizationType"],
    logGroup: ["name", "namePrefix"],
  },
  bucket: {
    bucket: ["bucket", "bucketPrefix"],
    publicAccessBlock: ["bucket"],
    cors: ["bucket"],
    uploadCompleterRole: ["name", "namePrefix", "assumeRolePolicy", "permissionsBoundary"],
    uploadCompleterLogGroup: ["name", "namePrefix"],
    uploadCompleter: [
      "runtime",
      "handler",
      "role",
      "s3Bucket",
      "s3Key",
      "environment",
      "loggingConfig",
    ],
    notification: ["bucket", "lambdaFunctions"],
  },
  postgres: {
    securityGroup: ["name", "namePrefix", "vpcId"],
    subnetGroup: ["name", "namePrefix", "subnetIds"],
    cluster: [
      "clusterIdentifier",
      "clusterIdentifierPrefix",
      "engine",
      "masterUsername",
      "masterPassword",
      "manageMasterUserPassword",
      "databaseName",
      "dbSubnetGroupName",
      "vpcSecurityGroupIds",
    ],
    instance: ["identifier", "identifierPrefix", "clusterIdentifier", "engine", "engineVersion"],
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
