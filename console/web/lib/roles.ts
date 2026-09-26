export const roles = ["member", "admin", "owner"] as const;

export type Role = (typeof roles)[number];

export function roleNamed(recorded: string): Role {
  const named = recorded.split(",").map((part) => part.trim());
  return named.includes("owner") ? "owner" : named.includes("admin") ? "admin" : "member";
}
