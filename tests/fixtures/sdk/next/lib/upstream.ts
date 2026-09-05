import { headers } from "next/headers";

export const RESUME_TAG = "résumé";

export async function upstreamCount(key: string, init: RequestInit): Promise<string> {
  const carried = await headers();
  const host = carried.get("host");
  if (!host) {
    throw new Error("the request carried no Host, so the upstream has no address");
  }
  const scheme = carried.get("x-forwarded-proto") ?? "https";
  const at = `${scheme}://${host}/api/next/upstream/${encodeURIComponent(key)}`;
  const res = await fetch(at, init);
  if (!res.ok) {
    throw new Error(`the upstream at ${at} answered ${res.status}`);
  }
  return res.text();
}
