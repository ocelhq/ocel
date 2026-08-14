import { Grant } from "../record";
import { LinkProperties } from "../registry";
import { Input } from "../stubs/pulumi";

type KnownToken = keyof LinkProperties;

export type LinkArgs<T extends string> = {
  type: T;
  properties: T extends KnownToken
    ? LinkProperties[T & KnownToken]
    : Record<string, Input<string>>;
  grants?: Grant[];
};

export declare class Link<T extends string> {
  constructor(name: string, args: LinkArgs<T>);
}

export declare function link<R, T extends string>(
  name: string,
  resource: R,
  fn: (resource: R) => LinkArgs<T>,
): Link<T>;
