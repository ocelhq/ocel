import { deleteProjectEnvValue, getProjectEnvValue, putProjectEnvValue } from "@console/api";

export async function GET(
  request: Request,
  { params }: { params: Promise<{ id: string; key: string }> },
) {
  const { id, key } = await params;
  return getProjectEnvValue(request, id, key);
}

export async function PUT(
  request: Request,
  { params }: { params: Promise<{ id: string; key: string }> },
) {
  const { id, key } = await params;
  return putProjectEnvValue(request, id, key);
}

export async function DELETE(
  request: Request,
  { params }: { params: Promise<{ id: string; key: string }> },
) {
  const { id, key } = await params;
  return deleteProjectEnvValue(request, id, key);
}
