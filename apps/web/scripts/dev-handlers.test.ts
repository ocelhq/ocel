import { beforeAll, describe, expect, it } from "vitest";
import { createTestSessionWithOrganization } from "../test/auth-harness";
import { setupTestDatabase } from "../test/db";
import { handleDevProjectConfig } from "./dev-handlers";

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
