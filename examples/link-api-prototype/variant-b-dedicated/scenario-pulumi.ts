import { Output, interpolate, secret } from "../stubs/pulumi";
import * as aws from "../stubs/pulumi-aws";
import { fromRdsInstance } from "./from";
import * as link from "./link";

declare const db: aws.rds.Instance;
declare const uploads: aws.s3.BucketV2;
declare const dbPassword: Output<string>;

link.postgres("main-db", fromRdsInstance(db, { password: secret(dbPassword) }));

link.custom("uploads", {
  type: "pulumi:aws.s3.BucketV2",
  properties: {
    bucket: uploads.bucket,
  },
  grants: [
    {
      actions: ["s3:GetObject", "s3:PutObject"],
      resources: [uploads.arn, interpolate`${uploads.arn}/*`],
    },
  ],
});
