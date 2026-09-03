import { randomUUID } from "node:crypto";
import { after } from "next/server";
import { bump } from "../../../../lib/state";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

export function GET() {
  const token = randomUUID();
  after(async () => {
    await bump(`after:${token}`);
  });
  return new Response(token, {
    headers: { "content-type": "text/plain; charset=utf-8", "cache-control": "no-store" },
  });
}
