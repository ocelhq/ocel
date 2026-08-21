/** A value SST accepts before its final value is available. */
export type Input<T> =
  | T
  | Promise<T>
  | { apply(f: (value: T) => unknown): unknown };

/** A provider-native permission an app receives along with the link's properties. */
export interface Grant {
  actions: string[];
  resources: Input<string>[];
  label?: string;
}

/** One entry of an SST link's `include`: the permissions SST hands its own consumers. */
export interface SSTInclude {
  type?: string;
  effect?: string;
  actions?: string[];
  resources?: string[];
}

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

export function scoped(
  name: string,
  grants: Grant[] | undefined,
): Grant[] | undefined {
  if (!grants || grants.length === 0) {
    return undefined;
  }
  for (const grant of grants) {
    if (grant.actions.length === 0 || grant.actions.some(unscopedAction)) {
      throw new Error(
        `link ${name} carries a grant over ${grant.actions.join(", ") || "no action"}: an action naming a whole service reaches past the resource the link names`,
      );
    }
    if (
      grant.resources.length === 0 ||
      grant.resources.some((resource) => resource === wildcard)
    ) {
      throw new Error(
        `link ${name} carries a grant over ${grant.resources.join(", ") || "no resource"}: an app receives permissions for the resource it links and nothing else`,
      );
    }
  }
  return grants;
}

/** Grants whose resource inputs reject wildcards when SST resolves them. */
export function scopedInputs(
  name: string,
  grants: Grant[] | undefined,
): Grant[] | undefined {
  const checked = scoped(name, grants);
  return checked?.map((grant) => ({
    ...grant,
    resources: grant.resources.map((resource) => scopedInput(name, resource)),
  }));
}

const awsPermission = "aws.permission";

const wildcard = "*";

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

function scopedInput(name: string, resource: Input<string>): Input<string> {
  if (typeof resource === "string") return scopedResource(name, resource);
  if (resource instanceof Promise) {
    return resource.then((value) => scopedResource(name, value));
  }
  return resource.apply((value) => scopedResource(name, value)) as Input<string>;
}

function scopedResource(name: string, resource: string): string {
  if (resource === wildcard) {
    throw new Error(
      `link ${name} carries a grant over ${wildcard}: an app receives permissions for the resource it links and nothing else`,
    );
  }
  return resource;
}
