export const runtime = "nodejs";
export const dynamic = "force-dynamic";

export function GET() {
  return Response.json({ ok: true, app: process.env.APP_NAME ?? "next" });
}
