import { Input } from "../stubs/pulumi";

declare module "../registry" {
  interface LinkProperties {
    "acme:kafka": {
      brokers: Input<string>;
      topic: Input<string>;
    };
  }
}

export {};
