import { interpolate } from "../stubs/pulumi";
import * as sst from "../stubs/sst";
import { Link, link } from "./link";

declare const vpc: sst.aws.Vpc;
declare const db: sst.aws.Postgres;
declare const uploads: sst.aws.Bucket;

export const mainDb = new Link("main-db", {
  type: "ocel:postgres",
  properties: {
    host: db.host,
    port: interpolate`${db.port}`,
    database: db.database,
    user: db.username,
    password: db.password,
  },
});

export const uploadsBucket = new Link("uploads", {
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

export const callbackForm = link("main-db-callback", db, (r) => ({
  type: "ocel:postgres",
  properties: {
    host: r.host,
    port: interpolate`${r.port}`,
    database: r.database,
    user: r.username,
    password: r.password,
  },
}));

export const augmentedCustomToken = new Link("events", {
  type: "acme:kafka",
  properties: {
    brokers: "b-1.example:9092",
    topic: "events",
  },
});
