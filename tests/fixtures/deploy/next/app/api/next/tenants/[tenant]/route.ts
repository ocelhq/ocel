import { echo } from "../../../../../lib/echo";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

const handler = echo(async ({ params }: { params: Promise<{ tenant: string }> }) => [
  (await params).tenant,
]);

export const GET = handler;
export const HEAD = handler;
export const OPTIONS = handler;
export const POST = handler;
export const PUT = handler;
export const PATCH = handler;
export const DELETE = handler;
