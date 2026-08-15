/** A provider-native permission an app receives along with the link's properties. */
export interface Grant {
  actions: string[];
  resources: string[];
  label?: string;
}

/** One consumable resource, as SST describes it, before it becomes a link record. */
export interface Linkable {
  name: string;
  urn: string;
  properties: Record<string, unknown>;
  grants?: Grant[];
}

/** One entry of an SST link's `include`: the permissions SST hands its own consumers. */
export interface SSTInclude {
  type?: string;
  effect?: string;
  actions?: string[];
  resources?: string[];
}

/**
 * The grants an SST link's `include` amounts to.
 *
 * SST's own permissions come through here rather than being dropped, so an app
 * consuming the link can reach the resource. An include ocel cannot express as
 * a scoped grant is refused: an app receiving values it has no permission to
 * use fails at runtime instead of at deploy.
 */
export function grantsFor(
  name: string,
  include: SSTInclude[] | undefined,
): Grant[] | undefined {
  if (!include || include.length === 0) {
    return undefined;
  }
  return include.map((entry) => {
    if (entry.type !== awsPermission) {
      throw new Error(
        `link ${name} includes a ${entry.type ?? "nameless"} permission, and ocel delivers a link's permissions as ${awsPermission} grants. Link the resource without it, or describe the grants the app needs yourself.`,
      );
    }
    if (entry.effect && entry.effect !== "allow") {
      throw new Error(
        `link ${name} includes a permission whose effect is ${entry.effect}; a link carries the permissions an app receives, never ones it is denied`,
      );
    }
    return {
      actions: entry.actions ?? [],
      resources: (entry.resources ?? []).map(String),
    };
  });
}

const awsPermission = "aws.permission";

/** What lands in the ocel store: a type token, a flat string bag, and grants. */
export interface LinkRecord {
  name: string;
  type: string;
  properties: Record<string, string>;
  grants?: Grant[];
}

const ocelNamespace = "ocel:";

const wildcard = "*";

/**
 * The type token a component publishes under, taken from its own Pulumi type.
 *
 * A `$`-chained urn names a resource some component is built out of, and a
 * composite links as one record per consumable resource with its constituents
 * invisible — so passing one is refused rather than published under the
 * composite's token with a constituent's properties.
 */
export function tokenFor(urn: string): string {
  const segments = urn.split("::");
  const own = segments[2];
  if (!own) {
    throw new Error(
      `${urn} names no component type, so a link published from it would have no token for a consumer to resolve it against`,
    );
  }
  if (own.includes("$")) {
    const [composite, ...constituents] = own.split("$");
    throw new Error(
      `${urn} names ${constituents.join("$")}, a resource ${composite} is built out of. Link ${composite} itself: a composite publishes one record per consumable resource, and its constituents are never linked individually.`,
    );
  }
  const [publisher, ...rest] = own.split(":");
  if (!publisher || rest.length === 0) {
    throw new Error(
      `component type ${own} carries no publisher namespace; a token is <publisher>:<type>`,
    );
  }
  return `${publisher}:${rest.join(".")}`;
}

/**
 * The link record one consumable resource publishes as.
 *
 * The bag is flat strings, the token comes from the component's own type, and
 * `ocel:` is refused: those tokens name what ocel's own provisioning produces,
 * and an app binds a typed client to them on that promise.
 */
export function recordFor(linkable: Linkable): LinkRecord {
  if (!linkable.name) {
    throw new Error(
      `the component at ${linkable.urn} is linked under no name; the name is what a consuming app binds to`,
    );
  }

  const type = tokenFor(linkable.urn);
  if (type.startsWith(ocelNamespace)) {
    throw new Error(
      `link ${linkable.name} would publish as ${type}, and the ${ocelNamespace} namespace names what ocel's own provisioning produces — an app binds a typed client to it on that promise. Publish under your own namespace instead.`,
    );
  }

  const record: LinkRecord = {
    name: linkable.name,
    type,
    properties: flatten(linkable.name, linkable.properties),
  };
  const grants = scoped(linkable.name, linkable.grants);
  if (grants) {
    record.grants = grants;
  }
  return record;
}

function flatten(
  name: string,
  properties: Record<string, unknown>,
): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [key, value] of Object.entries(properties)) {
    if (value === undefined || value === null) {
      continue;
    }
    if (!flat.has(typeof value)) {
      throw new Error(
        `link ${name} property ${key} is ${describe(value)}; a link record carries a flat bag of strings, so publish the pieces a consumer actually reads under their own keys`,
      );
    }
    out[key] = String(value);
  }
  return out;
}

const flat = new Set(["string", "number", "boolean", "bigint"]);

function describe(value: unknown): string {
  if (Array.isArray(value)) {
    return "a list";
  }
  if (typeof value === "object") {
    return "an object";
  }
  return `a ${typeof value}`;
}

function scoped(name: string, grants: Grant[] | undefined): Grant[] | undefined {
  if (!grants || grants.length === 0) {
    return undefined;
  }
  for (const grant of grants) {
    if (grant.actions.length === 0 || grant.actions.some(unscopedAction)) {
      throw new Error(
        `link ${name} carries a grant over ${grant.actions.join(", ") || "no action"}: an action naming a whole service reaches past the resource the link names`,
      );
    }
    if (grant.resources.length === 0 || grant.resources.includes(wildcard)) {
      throw new Error(
        `link ${name} carries a grant over ${grant.resources.join(", ") || "no resource"}: an app receives permissions for the resource it links and nothing else`,
      );
    }
  }
  return grants;
}

function unscopedAction(action: string): boolean {
  const separator = action.indexOf(":");
  if (separator < 0) {
    return action === wildcard;
  }
  return (
    action.slice(0, separator) === wildcard ||
    action.slice(separator + 1) === wildcard
  );
}
