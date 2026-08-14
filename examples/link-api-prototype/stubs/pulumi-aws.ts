import { Output } from "./pulumi";

export declare namespace rds {
  class Instance {
    address: Output<string>;
    port: Output<number>;
    dbName: Output<string>;
    username: Output<string>;
    password: Output<string | undefined>;
  }
}

export declare namespace s3 {
  class BucketV2 {
    bucket: Output<string>;
    arn: Output<string>;
  }
}
