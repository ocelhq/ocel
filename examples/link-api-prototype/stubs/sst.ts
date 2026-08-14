import { Output } from "./pulumi";

export declare namespace aws {
  class Vpc {
    id: Output<string>;
  }

  class Postgres {
    host: Output<string>;
    port: Output<number>;
    database: Output<string>;
    username: Output<string>;
    password: Output<string>;
  }

  class Bucket {
    name: Output<string>;
    arn: Output<string>;
  }
}
