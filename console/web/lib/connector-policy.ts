import type { Scope } from "@console/connectors";
import type { Ability } from "@ui/variables";

const byRole: Record<string, readonly Scope[]> = {
  owner: ["variables.read", "variables.write", "variables.reveal"],
  admin: ["variables.read", "variables.write"],
  member: ["variables.read"],
};

export function scopesFor(role: string, capabilities: readonly string[]): Scope[] {
  const granted = new Set<Scope>();
  for (const named of role.split(",")) {
    for (const scope of byRole[named.trim()] ?? []) {
      granted.add(scope);
    }
  }
  return [...granted].filter((scope) => capabilities.includes(scope));
}

export function abilityOf(scopes: readonly Scope[]): Ability {
  return {
    write: scopes.includes("variables.write"),
    reveal: scopes.includes("variables.reveal"),
  };
}
