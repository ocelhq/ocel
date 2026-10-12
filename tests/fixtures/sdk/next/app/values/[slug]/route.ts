import { createHash } from "node:crypto";
import { env } from "../../../infra/variables";

const sensitive = env.BUILD_SENSITIVE;
const secret = env.BUILD_SECRET;

function sha256(value: string): string {
  return createHash("sha256").update(value).digest("hex");
}

export async function GET(_request: Request, { params }: { params: Promise<{ slug: string }> }) {
  const { slug } = await params;
  return Response.json({ slug, sensitive: sha256(sensitive), secret: sha256(secret) });
}
