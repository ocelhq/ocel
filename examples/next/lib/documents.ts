import { pg } from "../ocel/index";

// Read side of the documents table. The write side lives in the uploader's
// onUploadComplete (see ocel/index.ts): the SDK has no get-file API yet, so
// this list route is how the app surfaces what has landed.
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
