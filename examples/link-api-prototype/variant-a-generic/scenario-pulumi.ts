import { Output, interpolate, secret } from "../stubs/pulumi";
import * as aws from "../stubs/pulumi-aws";
import { Link } from "./link";

declare const db: aws.rds.Instance;
declare const uploads: aws.s3.BucketV2;
declare const dbPassword: Output<string>;

export const mainDb = new Link("main-db", {
  type: "ocel:postgres",
  properties: {
    host: db.address,
    port: interpolate`${db.port}`,
    database: db.dbName,
    user: db.username,
    password: secret(dbPassword),
  },
});

export const uploadsBucket = new Link("uploads", {
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
