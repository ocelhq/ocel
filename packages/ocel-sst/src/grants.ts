/** A provider-native permission an app receives along with the binding's properties. */
export interface Grant {
  actions: string[];
  resources: string[];
  label?: string;
}

/** One entry of an SST binding's `include`: the permissions SST hands its own consumers. */
export interface SSTInclude {
  type?: string;
  effect?: string;
  actions?: string[];
  resources?: string[];
}

export function grantsFor(name: string, include: SSTInclude[] | undefined): Grant[] | undefined {
  if (!include || include.length === 0) {
    return undefined;
  }
  return include.map((entry) => {
    if (entry.type !== awsPermission) {
      throw new Error(
        `binding ${name} includes a ${entry.type ?? "nameless"} permission, and ocel delivers a binding's permissions as ${awsPermission} grants. Binding the resource without it, or describe the grants the app needs yourself.`,
      );
    }
    if (entry.effect && entry.effect !== "allow") {
      throw new Error(
        `binding ${name} includes a permission whose effect is ${entry.effect}; a binding carries the permissions an app receives, never ones it is denied`,
      );
    }
    return {
      actions: entry.actions ?? [],
      resources: (entry.resources ?? []).map(String),
    };
  });
}

export function scoped(name: string, grants: Grant[] | undefined): Grant[] | undefined {
  if (!grants || grants.length === 0) {
    return undefined;
  }
  for (const grant of grants) {
    if (grant.actions.length === 0 || grant.actions.some(unscopedAction)) {
      throw new Error(
        `binding ${name} carries a grant over ${grant.actions.join(", ") || "no action"}: an action naming a whole service reaches past the resource the binding names`,
      );
    }
    if (grant.resources.length === 0 || grant.resources.includes(wildcard)) {
      throw new Error(
        `binding ${name} carries a grant over ${grant.resources.join(", ") || "no resource"}: an app receives permissions for the resource it bindings and nothing else`,
      );
    }
  }
  return grants;
}

const awsPermission = "aws.permission";

const wildcard = "*";

function unscopedAction(action: string): boolean {
  const separator = action.indexOf(":");
  if (separator < 0) {
    return action === wildcard;
  }
  return action.slice(0, separator) === wildcard || action.slice(separator + 1) === wildcard;
}
