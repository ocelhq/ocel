import { headers } from "next/headers";

export const RESUME_TAG = "résumé";

export async function upstreamCount(key: string, init: RequestInit): Promise<string> {
  const incoming = await headers();
  const host = incoming.get("host");
  if (!host) {
    throw new Error("the request sent no Host, so the upstream has no address");
  }
  const scheme = incoming.get("x-forwarded-proto") ?? "https";
  const at = `${scheme}://${host}/api/next/upstream/${encodeURIComponent(key)}`;
  const res = await fetch(at, init);
  if (!res.ok) {
    throw new Error(`the upstream at ${at} answered ${res.status}`);
  }
  return res.text();
}
