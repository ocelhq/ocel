import { db } from "@repo/db";
import { resourceAssignment } from "@repo/db/schema";
import { beforeAll, describe, expect, it } from "vitest";
import { uuidv7 } from "uuidv7";
import { createTestSessionWithOrganization } from "../../../../test/auth-harness";
import { setupTestDatabase } from "../../../../test/db";
import { createProject } from "../../projects/route";
import { resolveResources } from "./route";

async function createProjectFor(
  session: {
    headers: Headers;
  },
  slug: string,
) {
  const response = await createProject(
    new Request("http://localhost/api/projects", {
      method: "POST",
      headers: {
        ...Object.fromEntries(session.headers),
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ name: "My Project", slug }),
    }),
  );
  return response.json();
}

function postRequest(body: unknown, headers: Headers) {
  return new Request("http://localhost/api/resources/resolve", {
    method: "POST",
    headers: {
      ...Object.fromEntries(headers),
      "Content-Type": "application/json",
    },
    body: JSON.stringify(body),
  });
}

describe("POST /api/resources/resolve", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("reuses an existing assignment: returns the stored connection string and no SQL is run against the cloud", async () => {
    const session = await createTestSessionWithOrganization();
    const previousAdminUrl = process.env.OCEL_CLOUD_ADMIN_URL;
    // Deliberately unreachable host/port - if resolve tried to actually
    // connect (i.e. ran SQL) instead of just formatting a connection
    // string, this test would time out or error.
    process.env.OCEL_CLOUD_ADMIN_URL =
      "postgres://cloud-admin:cloud-admin@cloud-host.invalid:5432/postgres";

    try {
      const created = await createProjectFor(session, "resolve-reuse");

      await db.insert(resourceAssignment).values({
        id: uuidv7(),
        userId: session.user.id,
        projectId: created.id,
        resourceName: "main",
        resourceType: "POSTGRES",
        config: { version: "17" },
        databaseName: "db_main_seed",
        roleName: "role_main_seed",
        password: "s3cret-pw",
      });

      const response = await resolveResources(
        postRequest(
          {
            projectId: created.id,
            resources: [{ name: "main", type: "POSTGRES", config: { version: "17" } }],
          },
          session.headers,
        ),
      );

      expect(response.status).toBe(200);
      const body = await response.json();

      expect(typeof body.expiresAt).toBe("string");
      expect(new Date(body.expiresAt).getTime()).toBeGreaterThan(Date.now());

      const raw = body.env.OCEL_RESOURCE_POSTGRES_main;
      expect(raw).toBeTruthy();
      const { connectionString } = JSON.parse(raw);
      const url = new URL(connectionString);
      expect(url.username).toBe("role_main_seed");
      expect(url.password).toBe("s3cret-pw");
      expect(url.pathname).toBe("/db_main_seed");
      expect(url.hostname).toBe("cloud-host.invalid");
    } finally {
      if (previousAdminUrl === undefined) {
        delete process.env.OCEL_CLOUD_ADMIN_URL;
      } else {
        process.env.OCEL_CLOUD_ADMIN_URL = previousAdminUrl;
      }
      await session.cleanup();
    }
  });

  it("returns 501 when no assignment exists yet for the resource", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const created = await createProjectFor(session, "resolve-no-assignment");

      const response = await resolveResources(
        postRequest(
          {
            projectId: created.id,
            resources: [{ name: "main", type: "POSTGRES", config: {} }],
          },
          session.headers,
        ),
      );

      expect(response.status).toBe(501);
    } finally {
      await session.cleanup();
    }
  });

  it("returns 400 for an unregistered resource type", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const created = await createProjectFor(session, "resolve-bad-type");

      const response = await resolveResources(
        postRequest(
          {
            projectId: created.id,
            resources: [{ name: "main", type: "REDIS", config: {} }],
          },
          session.headers,
        ),
      );

      expect(response.status).toBe(400);
    } finally {
      await session.cleanup();
    }
  });

  it("returns 400 for an invalid body", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const response = await resolveResources(
        postRequest({ projectId: "" }, session.headers),
      );

      expect(response.status).toBe(400);
    } finally {
      await session.cleanup();
    }
  });

  it("returns 404 for a Project in an org the caller doesn't belong to", async () => {
    const session = await createTestSessionWithOrganization();
    const otherSession = await createTestSessionWithOrganization();

    try {
      const created = await createProjectFor(session, "resolve-not-your-org");

      const response = await resolveResources(
        postRequest(
          {
            projectId: created.id,
            resources: [{ name: "main", type: "POSTGRES", config: {} }],
          },
          otherSession.headers,
        ),
      );

      expect(response.status).toBe(404);
    } finally {
      await session.cleanup();
      await otherSession.cleanup();
    }
  });

  it("returns 404 for a non-existent project id", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const response = await resolveResources(
        postRequest(
          {
            projectId: "00000000-0000-7000-8000-000000000000",
            resources: [{ name: "main", type: "POSTGRES", config: {} }],
          },
          session.headers,
        ),
      );

      expect(response.status).toBe(404);
    } finally {
      await session.cleanup();
    }
  });

  it("returns 401 when unauthenticated", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const created = await createProjectFor(session, "resolve-unauthed");

      const response = await resolveResources(
        postRequest(
          {
            projectId: created.id,
            resources: [{ name: "main", type: "POSTGRES", config: {} }],
          },
          new Headers(),
        ),
      );

      expect(response.status).toBe(401);
    } finally {
      await session.cleanup();
    }
  });
});
