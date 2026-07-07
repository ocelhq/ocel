import { Client } from "pg";
import { beforeAll, describe, expect, it } from "vitest";
import { createTestSessionWithOrganization } from "../test/auth-harness";
import { setupTestDatabase } from "../test/db";
import { handleRequest } from "./local-api-server";

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

describe("handleRequest", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("mounts the real resolve handler at POST /api/resources/resolve, resolving to a real per-user db the caller can connect to", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const createResponse = await handleRequest(
        postRequest(
          "/api/projects",
          { name: "Local Harness Project", slug: "local-harness-e2e" },
          session.headers,
        ),
      );
      expect(createResponse.status).toBe(201);
      const project = await createResponse.json();

      const resolveResponse = await handleRequest(
        postRequest(
          "/api/resources/resolve",
          {
            projectId: project.id,
            resources: [{ name: "main", type: "POSTGRES", config: {} }],
          },
          session.headers,
        ),
      );

      expect(resolveResponse.status).toBe(200);
      const body = await resolveResponse.json();
      const { connectionString } = JSON.parse(
        body.env.OCEL_RESOURCE_POSTGRES_main,
      );

      // Proves resolve actually provisioned a real role + database on the
      // harness's own real DB connection, not a stub: connect with the
      // returned credentials and run a query against them.
      const client = new Client({ connectionString });
      await client.connect();
      try {
        const result = await client.query(
          "select current_database() as db, current_user as role",
        );
        const url = new URL(connectionString);
        expect(result.rows[0].db).toBe(url.pathname.slice(1));
        expect(result.rows[0].role).toBe(url.username);
      } finally {
        await client.end();
      }
    } finally {
      await session.cleanup();
    }
  });

  it("no longer serves the removed dev-only POST /dev/provision passthrough", async () => {
    const response = await handleRequest(
      postRequest("/dev/provision", { resources: [] }),
    );
    expect(response.status).toBe(404);
  });

  it("still serves the dev-only POST /dev/project-config passthrough", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const response = await handleRequest(
        postRequest(
          "/dev/project-config",
          { projectId: "proj_1" },
          session.headers,
        ),
      );
      expect(response.status).toBe(200);
      const body = await response.json();
      expect(body.userId).toBe(session.user.id);
    } finally {
      await session.cleanup();
    }
  });
});
