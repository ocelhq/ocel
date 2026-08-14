export interface Pg {
  id: string;
  kind: "postgres";
}

export function postgres(id: string): Pg {
  return { id, kind: "postgres" };
}
