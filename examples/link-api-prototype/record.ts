import { Input } from "./stubs/pulumi";

export type Grant = {
  actions: [string, ...string[]];
  resources: [Input<string>, ...Input<string>[]];
  label?: string;
};

export type LinkRecord = {
  name: string;
  type: string;
  properties: Record<string, Input<string>>;
  grants: Grant[];
};
