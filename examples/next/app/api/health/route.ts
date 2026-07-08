// Readiness probe the e2e harness polls before hitting the CRUD routes.
export const runtime = "nodejs";

export function GET() {
  return Response.json({ ok: true });
}
