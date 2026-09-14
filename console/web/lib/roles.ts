export const roles = ["member", "admin", "owner"] as const;

export type Role = (typeof roles)[number];

export function roleNamed(held: string): Role {
  const named = held.split(",").map((part) => part.trim());
  return named.includes("owner") ? "owner" : named.includes("admin") ? "admin" : "member";
}
