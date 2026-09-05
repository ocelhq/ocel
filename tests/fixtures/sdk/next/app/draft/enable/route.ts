import { draftMode } from "next/headers";
import { redirect } from "next/navigation";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

export async function GET() {
  (await draftMode()).enable();
  redirect("/draft");
}
