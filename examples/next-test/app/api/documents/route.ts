export const runtime = "nodejs";

export async function GET() {
  return Response.json([{ id: "1", doc: "some-document" }]);
}
