import { getDeployment } from "@console/api";

export async function GET(
  request: Request,
  { params }: { params: Promise<{ id: string; deploymentId: string }> },
) {
  const { id, deploymentId } = await params;
  return getDeployment(request, id, deploymentId);
}
