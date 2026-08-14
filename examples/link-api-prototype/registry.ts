import { Input } from "./stubs/pulumi";

export interface LinkProperties {
  "ocel:postgres": {
    host: Input<string>;
    port: Input<string>;
    database: Input<string>;
    user: Input<string>;
    password: Input<string>;
  };
}
