import { getProjectById } from "@console/api";

export async function GET(
  request: Request,
  { params }: { params: Promise<{ id: string }> },
) {
  const { id } = await params;
  return getProjectById(request, id);
}
