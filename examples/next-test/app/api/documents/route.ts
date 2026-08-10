import { env } from "@/ocel/vars";

export const runtime = "nodejs";

export async function GET() {
  return Response.json([
    {
      id: "1",
      doc: "some-document",
      vars: {
        stripe: env.STRIPE_API_KEY,
        posthog: env.POSTHOG_PROJECT_ID,
        superSecret: env.SUPER_SECRET_VALUE,
      },
      processEnv: process.env,
    },
  ]);
}
