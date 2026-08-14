export interface Bucket {
  id: string;
  kind: "bucket";
}

export function bucket(id: string): Bucket {
  return { id, kind: "bucket" };
}
