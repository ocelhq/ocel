import { listDocuments } from "../../../lib/documents";

export const runtime = "nodejs";

export async function GET() {
  return Response.json(await listDocuments());
}
