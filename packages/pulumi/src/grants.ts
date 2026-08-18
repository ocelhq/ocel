/** A provider-native permission an app receives along with the link's properties. */
export interface Grant {
  actions: string[];
  resources: string[];
  label?: string;
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
    if (grant.resources.length === 0 || grant.resources.includes(wildcard)) {
      throw new Error(
        `link ${name} carries a grant over ${grant.resources.join(", ") || "no resource"}: an app receives permissions for the resource it links and nothing else`,
      );
    }
  }
  return grants;
}

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
