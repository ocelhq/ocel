import type { Scope } from "@console/connectors";

const byRole: Record<string, readonly Scope[]> = {
  owner: ["envvars.read", "envvars.write", "envvars.reveal"],
  admin: ["envvars.read", "envvars.write"],
  member: ["envvars.read"],
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
