export const runtime = "edge";

export function GET() {
  return Response.json({
    runtime: "edge",
    posthog: process.env.POSTHOG_PROJECT_ID ?? null,
    renderedAt: Date.now(),
  });
}
