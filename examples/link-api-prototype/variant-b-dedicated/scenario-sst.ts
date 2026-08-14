import { interpolate } from "../stubs/pulumi";
import * as sst from "../stubs/sst";
import { fromSstPostgres } from "./from";
import * as link from "./link";

declare const db: sst.aws.Postgres;
declare const uploads: sst.aws.Bucket;

link.postgres("main-db", fromSstPostgres(db));

link.postgres("main-db-explicit", {
  host: db.host,
  port: interpolate`${db.port}`,
  database: db.database,
  user: db.username,
  password: db.password,
});

link.custom("uploads", {
  type: "sst:aws.Bucket",
  properties: {
    bucket: uploads.name,
  },
  grants: [
    {
      actions: ["s3:GetObject", "s3:PutObject", "s3:ListBucket"],
      resources: [uploads.arn, interpolate`${uploads.arn}/*`],
      label: "read/write objects",
    },
  ],
});
