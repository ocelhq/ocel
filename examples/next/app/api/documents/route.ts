import { listDocuments } from "../../../lib/documents";

export async function GET() {
  return Response.json(await listDocuments());
}
