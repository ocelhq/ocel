import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { createTestSessionWithOrganization } from "../test/auth-harness";
import { setupTestDatabase } from "../test/db";
import { handleDevProjectConfig, handleDevProvision } from "./dev-handlers";

function postRequest(path: string, body: unknown, headers?: Headers) {
  return new Request(`http://localhost${path}`, {
    method: "POST",
    headers: {
      ...(headers ? Object.fromEntries(headers) : {}),
      "Content-Type": "application/json",
    },
    body: JSON.stringify(body),
  });
}

describe("POST /dev/project-config", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("returns 401 without a valid Bearer session", async () => {
    const response = await handleDevProjectConfig(
      postRequest("/dev/project-config", { projectId: "proj_1" }),
    );

    expect(response.status).toBe(401);
  });

  it("echoes the caller's real user/org identity for a valid Bearer session", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const response = await handleDevProjectConfig(
        postRequest(
          "/dev/project-config",
          { projectId: "proj_1" },
          session.headers,
        ),
      );

      expect(response.status).toBe(200);
      const body = await response.json();
      expect(body.userId).toBe(session.user.id);
      expect(body.orgId).toBe(session.organization.id);
      expect(body.projectId).toBe("proj_1");
    } finally {
      await session.cleanup();
    }
  });
});

describe("POST /dev/provision", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  const originalDatabaseUrl = process.env.DATABASE_URL;
  afterEach(() => {
    process.env.DATABASE_URL = originalDatabaseUrl;
  });

  it("returns 401 without a valid Bearer session", async () => {
    const response = await handleDevProvision(
      postRequest("/dev/provision", { resources: [] }),
    );

    expect(response.status).toBe(401);
  });

  it("resolves each resource to the shared DATABASE_URL", async () => {
    const session = await createTestSessionWithOrganization();
    process.env.DATABASE_URL = "postgres://stub:stub@localhost:5432/stub";

    try {
      const response = await handleDevProvision(
        postRequest(
          "/dev/provision",
          { resources: [{ name: "main", type: "POSTGRES" }] },
          session.headers,
        ),
      );

      expect(response.status).toBe(200);
      const body = await response.json();
      expect(body).toEqual([
        {
          name: "main",
          type: "POSTGRES",
          env: {
            OCEL_RESOURCE_POSTGRES_main: JSON.stringify({
              connectionString: "postgres://stub:stub@localhost:5432/stub",
            }),
          },
        },
      ]);
    } finally {
      await session.cleanup();
    }
  });

  it("returns a clear error when DATABASE_URL is unset", async () => {
    const session = await createTestSessionWithOrganization();
    delete process.env.DATABASE_URL;

    try {
      const response = await handleDevProvision(
        postRequest(
          "/dev/provision",
          { resources: [{ name: "main", type: "POSTGRES" }] },
          session.headers,
        ),
      );

      expect(response.status).toBe(500);
      const body = await response.json();
      expect(body.error).toMatch(/DATABASE_URL/);
    } finally {
      await session.cleanup();
    }
  });
});
