import { getActiveOrganizationSession, getSessionUserId } from "@repo/auth";

export function parseProjectConfigRequest(
  body: unknown,
): { projectId: string } | null {
  if (typeof body !== "object" || body === null) {
    return null;
  }
  const { projectId } = body as { projectId?: unknown };
  if (typeof projectId !== "string" || projectId.length === 0) {
    return null;
  }
  return { projectId };
}

async function readJsonBody(request: Request): Promise<unknown> {
  try {
    return await request.json();
  } catch {
    return undefined;
  }
}

// Dev-only passthrough: authenticates the caller and echoes back their real
// user/org identity. There is no real per-project provisioning yet - that
// waits on the real Ocel API.
export async function handleDevProjectConfig(
  request: Request,
): Promise<Response> {
  const userId = await getSessionUserId(request.headers);
  if (!userId) {
    return Response.json({ error: "Unauthorized" }, { status: 401 });
  }

  const parsed = parseProjectConfigRequest(await readJsonBody(request));
  if (!parsed) {
    return Response.json(
      { error: "Invalid request: projectId is required" },
      { status: 400 },
    );
  }

  const activeOrganizationSession = await getActiveOrganizationSession(
    request.headers,
  );

  return Response.json(
    {
      orgId: activeOrganizationSession?.activeOrganizationId ?? "",
      projectId: parsed.projectId,
      userId,
      envVars: {},
    },
    { status: 200 },
  );
}
