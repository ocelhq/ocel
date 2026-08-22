import { cacheLife, cacheTag } from "next/cache";
import { connection } from "next/server";

async function payload() {
  "use cache";
  cacheLife("editorial");
  cacheTag("example-cache");
  return { producedAt: new Date().toISOString() };
}

export async function GET() {
  const cached = await payload();
  await connection();
  return Response.json({
    ...cached,
    servedAt: new Date().toISOString(),
  });
}
