import { env } from "../../../src/env";

export const dynamic = "force-dynamic";

export function GET() {
  return Response.json({
    STRIPE_KEY: env.STRIPE_KEY,
    NEXT_PUBLIC_API_URL: env.NEXT_PUBLIC_API_URL,
    NEXT_PUBLIC_RETRIES: env.NEXT_PUBLIC_RETRIES,
    defined: process.env.OCEL_PUBLIC_ENV ?? null,
  });
}
