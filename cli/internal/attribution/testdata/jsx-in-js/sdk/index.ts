export function postgres(id: string) {
  return { id, kind: "postgres" as const };
}
