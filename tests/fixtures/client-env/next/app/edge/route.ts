import { env } from "../../infra/env";

export const runtime = "edge";
export const dynamic = "force-dynamic";

export function GET() {
  return Response.json({ publicValue: env.NEXT_PUBLIC_GREETING });
}
