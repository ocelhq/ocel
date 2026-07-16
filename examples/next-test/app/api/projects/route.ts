export const runtime = "nodejs";

export const revalidate = 60;

export async function GET() {
  return Response.json([{ id: "201", doc: "project" }]);
}
