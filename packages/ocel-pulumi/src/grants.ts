import type { Input } from "@pulumi/pulumi";

/**
 * A provider-native permission a deploy gives the apps bound to a binding, published beside its
 * properties: actions over resources, as AWS grants, or one role on one resource, as Google
 * Cloud grants.
 */
export type Grant = ActionGrant | RoleGrant;

/** Actions over resources, as an AWS IAM policy statement names them. */
export interface ActionGrant {
  actions: string[];
  resources: string[];
  label?: string;
}

/**
 * One role on one resource, as a Google Cloud IAM binding names them: `role` is `roles/<role>`
 * or a custom role's full name, and `resource` the resource's name, such as `projects/<id>` or
 * `projects/<project>/serviceAccounts/<email>`.
 */
export interface RoleGrant {
  role: string;
  resource: string;
  label?: string;
}

/**
 * A grant as it is written in a Pulumi program, where the resource a permission
 * names is an output of the resource being bound and is resolved before the
 * record is published.
 */
export type GrantInput = ActionGrantInput | RoleGrantInput;

/** Actions over resources, each resource an input resolved before the record is published. */
export interface ActionGrantInput {
  actions: string[];
  resources: Input<string>[];
  label?: string;
}

/** One role on one resource, the resource an input resolved before the record is published. */
export interface RoleGrantInput {
  role: string;
  resource: Input<string>;
  label?: string;
}

export function scoped(name: string, grants: Grant[] | undefined): Grant[] | undefined {
  if (!grants || grants.length === 0) {
    return undefined;
  }
  for (const grant of grants) {
    if (isRoleGrant(grant)) {
      roleScoped(name, grant);
    } else {
      actionScoped(name, grant);
    }
  }
  return grants;
}

function isRoleGrant(grant: Grant): grant is RoleGrant {
  return "role" in grant || "resource" in grant;
}

function roleScoped(name: string, grant: RoleGrant): void {
  if ("actions" in grant || "resources" in grant) {
    throw new Error(
      `binding ${name} includes a grant of ${grant.role} that also names actions: a grant is a role on one resource or actions over resources, never both`,
    );
  }
  if (!grant.role || grant.role === wildcard) {
    throw new Error(
      `binding ${name} includes a grant of ${JSON.stringify(grant.role ?? null)} on ${grant.resource}: "*" is every role, and a grant names the role the app needs`,
    );
  }
  if (!grant.resource || grant.resource === wildcard) {
    throw new Error(
      `binding ${name} includes a grant of ${grant.role} on ${grant.resource || "no resource"}: an app receives permissions for the resource it binds and nothing else`,
    );
  }
}

function actionScoped(name: string, grant: ActionGrant): void {
  if (grant.actions.length === 0 || grant.actions.some(unscopedAction)) {
    throw new Error(
      `binding ${name} includes a grant over ${grant.actions.join(", ") || "no action"}: an action naming a whole service reaches past the resource the binding names`,
    );
  }
  if (grant.resources.length === 0 || grant.resources.includes(wildcard)) {
    throw new Error(
      `binding ${name} includes a grant over ${grant.resources.join(", ") || "no resource"}: an app receives permissions for the resource it binds and nothing else`,
    );
  }
}

const wildcard = "*";

function unscopedAction(action: string): boolean {
  const separator = action.indexOf(":");
  if (separator < 0) {
    return action === wildcard;
  }
  return action.slice(0, separator) === wildcard || action.slice(separator + 1) === wildcard;
}
