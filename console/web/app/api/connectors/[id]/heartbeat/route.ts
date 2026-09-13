import { connectorHeartbeat } from "@console/api";

export async function POST(request: Request, { params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  return connectorHeartbeat(request, id);
}
