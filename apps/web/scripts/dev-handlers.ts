import { getActiveOrganizationSession, getSessionUserId } from "@repo/auth";

type ResourceEntry = { name: string; type: string };

type ProvisionedResourceWire = {
  name: string;
  type: string;
  env: Record<string, string>;
};

function isResourceEntry(value: unknown): value is ResourceEntry {
  return (
    typeof value === "object" &&
    value !== null &&
    typeof (value as ResourceEntry).name === "string" &&
    typeof (value as ResourceEntry).type === "string"
  );
}

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

export function parseProvisionRequest(
  body: unknown,
): { resources: ResourceEntry[] } | null {
  if (typeof body !== "object" || body === null) {
    return null;
  }
  const { resources } = body as { resources?: unknown };
  if (!Array.isArray(resources) || !resources.every(isResourceEntry)) {
    return null;
  }
  return { resources };
}

// Matches internal/provision.ResourceTypeName's wire format:
// OCEL_RESOURCE_<TYPE>_<name> -> JSON {connectionString} (see
// packages/ocel/src/utils/get-config.ts), so a resource resolves the same
// way whether it came from the real Ocel API or this local passthrough.
export function buildResourceEnv(
  resource: ResourceEntry,
  connectionString: string,
): Record<string, string> {
  const key = `OCEL_RESOURCE_${resource.type}_${resource.name}`;
  return { [key]: JSON.stringify({ connectionString }) };
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

// Dev-only passthrough: resolves every declared resource to the same shared
// DATABASE_URL instead of provisioning a real per-project instance.
export async function handleDevProvision(request: Request): Promise<Response> {
  const userId = await getSessionUserId(request.headers);
  if (!userId) {
    return Response.json({ error: "Unauthorized" }, { status: 401 });
  }

  const parsed = parseProvisionRequest(await readJsonBody(request));
  if (!parsed) {
    return Response.json(
      {
        error: "Invalid request: resources must be an array of {name, type}",
      },
      { status: 400 },
    );
  }

  const databaseUrl = process.env.DATABASE_URL;
  if (!databaseUrl) {
    return Response.json(
      { error: "DATABASE_URL is not set - cannot provision resources locally" },
      { status: 500 },
    );
  }

  const provisioned: ProvisionedResourceWire[] = parsed.resources.map(
    (resource) => ({
      name: resource.name,
      type: resource.type,
      env: buildResourceEnv(resource, databaseUrl),
    }),
  );

  return Response.json(provisioned, { status: 200 });
}
