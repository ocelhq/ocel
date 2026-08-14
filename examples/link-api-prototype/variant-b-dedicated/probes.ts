import { interpolate } from "../stubs/pulumi";
import * as sst from "../stubs/sst";
import * as link from "./link";

declare const db: sst.aws.Postgres;

// @ts-expect-error
link.postgress("main-db", {
  host: db.host,
  port: interpolate`${db.port}`,
  database: db.database,
  user: db.username,
  password: db.password,
});

// @ts-expect-error
link.postgres("main-db", {
  host: db.host,
  port: interpolate`${db.port}`,
  database: db.database,
  user: db.username,
});

link.postgres("main-db", {
  host: db.host,
  port: interpolate`${db.port}`,
  database: db.database,
  user: db.username,
  password: db.password,
  // @ts-expect-error
  passwrod: db.password,
});
