import type { OwnedProperties, OwnedToken } from "./registry.js";
import { resolve } from "./resolve.js";

export { LinkError } from "./resolve.js";

type OwnedShapes = { [TToken in OwnedToken]: OwnedProperties<TToken> };

/**
 * The properties each link type carries, keyed by its type token.
 *
 * Tokens ocel owns are already here. Type a token from your own publisher by
 * augmenting this interface — there is no generation step:
 *
 * ```ts
 * declare module "ocel/link" {
 *   interface LinkProperties {
 *     "acme:kafka": { brokers: string; topic: string };
 *   }
 * }
 * ```
 */
export interface LinkProperties extends OwnedShapes {}

/**
 * The properties a token resolves to: its declared shape if the token is known,
 * otherwise a free-form bag of strings.
 */
export type PropertiesOf<TToken extends string> =
  TToken extends keyof LinkProperties
    ? LinkProperties[TToken]
    : Record<string, string>;

/**
 * Reads the links this app consumes, whoever provisioned them.
 *
 * Values arrive after the discovery pass, so reading a property while ocel is
 * still collecting declarations throws.
 */
export const link = {
  /**
   * The properties of a postgres link. Throws unless the named link was
   * delivered and carries an `ocel:postgres` record.
   */
  postgres(name: string): LinkProperties["ocel:postgres"] {
    return resolve(name, "ocel:postgres") as LinkProperties["ocel:postgres"];
  },

  /**
   * The properties of a bucket link. Throws unless the named link was
   * delivered and carries an `ocel:bucket` record.
   */
  bucket(name: string): LinkProperties["ocel:bucket"] {
    return resolve(name, "ocel:bucket") as LinkProperties["ocel:bucket"];
  },

  /**
   * The properties of a link under the type token you name — the only path to
   * a token ocel ships no accessor for.
   *
   * A token ocel owns is checked against its declared shape; any other token is
   * handed back as the publisher wrote it, typed by whatever you augmented onto
   * {@link LinkProperties}. Either way a record carrying a different token than
   * the one you name is refused.
   */
  custom<const TToken extends string>(
    name: string,
    type: TToken,
  ): PropertiesOf<TToken> {
    return resolve(name, type) as PropertiesOf<TToken>;
  },
};
