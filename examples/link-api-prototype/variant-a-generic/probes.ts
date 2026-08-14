import { interpolate } from "../stubs/pulumi";
import * as sst from "../stubs/sst";
import { Link } from "./link";

declare const db: sst.aws.Postgres;

export const missingPasswordRejected = new Link("main-db", {
  type: "ocel:postgres",
  // @ts-expect-error
  properties: {
    host: db.host,
    port: interpolate`${db.port}`,
    database: db.database,
    user: db.username,
  },
});

export const propertyTypoRejected = new Link("main-db", {
  type: "ocel:postgres",
  properties: {
    host: db.host,
    port: interpolate`${db.port}`,
    database: db.database,
    user: db.username,
    password: db.password,
    // @ts-expect-error
    passwrod: db.password,
  },
});

export const typoTokenSilentlyDegrades = new Link("main-db", {
  type: "ocel:postgress",
  properties: {
    host: db.host,
    prot: interpolate`${db.port}`,
  },
});

export const numberPropertyRejected = new Link("main-db", {
  type: "sst:aws.Postgres",
  properties: {
    // @ts-expect-error
    port: db.port,
  },
});
