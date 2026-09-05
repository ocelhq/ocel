import { pg } from "../../../ocel/index";

export type Document = {
  id: number;
  key: string;
  name: string;
  mime_type: string;
  size: string;
  owner_id: string | null;
};

export async function listDocuments(): Promise<Document[]> {
  const { rows } = await pg.query<Document>(
    "SELECT id, key, name, mime_type, size, owner_id FROM documents ORDER BY id",
  );
  return rows;
}
