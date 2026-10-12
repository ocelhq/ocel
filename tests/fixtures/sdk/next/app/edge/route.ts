import { env } from "../../infra/variables";

export const runtime = "edge";
export const dynamic = "force-dynamic";

export function GET() {
  return Response.json({ publicValue: env.NEXT_PUBLIC_GREETING });
}
