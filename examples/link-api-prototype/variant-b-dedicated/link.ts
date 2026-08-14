import { Grant } from "../record";
import { LinkProperties } from "../registry";
import { Input } from "../stubs/pulumi";

export declare function postgres(
  name: string,
  properties: LinkProperties["ocel:postgres"],
): void;

export declare function custom(
  name: string,
  args: {
    type: string;
    properties: Record<string, Input<string>>;
    grants?: Grant[];
  },
): void;
