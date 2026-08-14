import type { OwnedProperties } from "./registry.js";
import { resolve } from "./resolve.js";

export { LinkError } from "./resolve.js";

export interface LinkProperties {
  "ocel:postgres": OwnedProperties<"ocel:postgres">;
  "ocel:bucket": OwnedProperties<"ocel:bucket">;
}

export type PropertiesOf<TToken extends string> =
  TToken extends keyof LinkProperties
    ? LinkProperties[TToken]
    : Record<string, string>;

export const link = {
  postgres(name: string): LinkProperties["ocel:postgres"] {
    return resolve(name, "ocel:postgres") as LinkProperties["ocel:postgres"];
  },

  bucket(name: string): LinkProperties["ocel:bucket"] {
    return resolve(name, "ocel:bucket") as LinkProperties["ocel:bucket"];
  },

  custom<const TToken extends string>(
    name: string,
    type: TToken,
  ): PropertiesOf<TToken> {
    return resolve(name, type) as PropertiesOf<TToken>;
  },
};
