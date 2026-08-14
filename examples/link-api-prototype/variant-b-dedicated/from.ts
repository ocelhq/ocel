import { LinkProperties } from "../registry";
import { Input } from "../stubs/pulumi";
import * as aws from "../stubs/pulumi-aws";
import * as sst from "../stubs/sst";

export declare function fromSstPostgres(
  db: sst.aws.Postgres,
): LinkProperties["ocel:postgres"];

export declare function fromRdsInstance(
  db: aws.rds.Instance,
  auth: { password: Input<string> },
): LinkProperties["ocel:postgres"];
