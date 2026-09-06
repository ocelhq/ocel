import { beforeAll, describe, expect, it } from "vitest";
import { createTestSessionWithOrganization } from "../../../../../test/auth-harness";
import { setupTestDatabase } from "../../../../../test/db";
import { createProject } from "../../route";
import { deleteProjectEnvValue, getProjectEnvValue, putProjectEnvValue } from "./[key]/route";
import { listProjectEnv } from "./route";

async function createProjectFor(
  session: { headers: Headers },
  slug: string,
): Promise<{ id: string }> {
  const response = await createProject(
    new Request("http://localhost/api/projects", {
      method: "POST",
      headers: { ...Object.fromEntries(session.headers), "Content-Type": "application/json" },
      body: JSON.stringify({ name: "My Project", slug }),
    }),
  );
  return (await response.json()) as { id: string };
}

function putRequest(headers: Headers, value: unknown) {
  return new Request("http://localhost/api/projects/x/env/K", {
    method: "PUT",
    headers: { ...Object.fromEntries(headers), "Content-Type": "application/json" },
    body: JSON.stringify({ value }),
  });
}

function deleteRequest(headers: Headers) {
  return new Request("http://localhost/api/projects/x/env/K", { method: "DELETE", headers });
}

function getRequest(headers: Headers) {
  return new Request("http://localhost/api/projects/x/env/K", { headers });
}

function listRequest(headers: Headers) {
  return new Request("http://localhost/api/projects/x/env", { headers });
}

const OTHER_ORG_PROJECT = "00000000-0000-7000-8000-000000000000";

describe("project env values", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("lists what was put, newest value per key", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "env-store");

      expect(
        (await putProjectEnvValue(putRequest(session.headers, "hello"), created.id, "GREETING"))
          .status,
      ).toBe(200);
      expect(
        (await putProjectEnvValue(putRequest(session.headers, "again"), created.id, "GREETING"))
          .status,
      ).toBe(200);
      expect(
        (await putProjectEnvValue(putRequest(session.headers, "t0ken"), created.id, "SECRET_TOKEN"))
          .status,
      ).toBe(200);

      const response = await listProjectEnv(listRequest(session.headers), created.id);
      expect(response.status).toBe(200);
      expect(await response.json()).toMatchObject([
        { key: "GREETING", value: "again" },
        { key: "SECRET_TOKEN", value: "t0ken" },
      ]);
    } finally {
      await session.cleanup();
    }
  });

  it("carries the values `ocel dev` resolves for the linked project alone", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const mine = await createProjectFor(session, "env-mine");
      const other = await createProjectFor(session, "env-other");

      await putProjectEnvValue(putRequest(session.headers, "mine"), mine.id, "GREETING");
      await putProjectEnvValue(putRequest(session.headers, "theirs"), other.id, "GREETING");

      const response = await listProjectEnv(listRequest(session.headers), mine.id);
      expect(await response.json()).toEqual([
        { key: "GREETING", value: "mine", updatedAt: expect.any(Number) },
      ]);
    } finally {
      await session.cleanup();
    }
  });

  it("deletes a value and reports whether one was held", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "env-delete");
      await putProjectEnvValue(putRequest(session.headers, "hello"), created.id, "GREETING");

      const removed = await deleteProjectEnvValue(
        deleteRequest(session.headers),
        created.id,
        "GREETING",
      );
      expect(removed.status).toBe(200);
      expect(await removed.json()).toEqual({ deleted: true });

      const again = await deleteProjectEnvValue(
        deleteRequest(session.headers),
        created.id,
        "GREETING",
      );
      expect(await again.json()).toEqual({ deleted: false });

      const response = await listProjectEnv(listRequest(session.headers), created.id);
      expect(await response.json()).toEqual([]);
    } finally {
      await session.cleanup();
    }
  });

  it("answers one key on its own, without listing the store", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "env-one-key");
      await putProjectEnvValue(putRequest(session.headers, "hello"), created.id, "GREETING");
      await putProjectEnvValue(putRequest(session.headers, "t0ken"), created.id, "SECRET_TOKEN");

      const response = await getProjectEnvValue(
        getRequest(session.headers),
        created.id,
        "GREETING",
      );
      expect(response.status).toBe(200);
      expect(await response.json()).toEqual({
        key: "GREETING",
        value: "hello",
        updatedAt: expect.any(Number),
      });
    } finally {
      await session.cleanup();
    }
  });

  it("answers 404 for a key the project does not hold", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "env-no-such-key");

      const response = await getProjectEnvValue(
        getRequest(session.headers),
        created.id,
        "GREETING",
      );
      expect(response.status).toBe(404);
    } finally {
      await session.cleanup();
    }
  });

  it("refuses a name that is not a variable name", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "env-bad-key");

      for (const verb of [getProjectEnvValue, putProjectEnvValue, deleteProjectEnvValue]) {
        const response = await verb(putRequest(session.headers, "hello"), created.id, "not a key");
        expect(response.status).toBe(400);
      }
    } finally {
      await session.cleanup();
    }
  });

  it("refuses a body without a string value", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "env-bad-body");

      const response = await putProjectEnvValue(
        putRequest(session.headers, 42),
        created.id,
        "GREETING",
      );
      expect(response.status).toBe(400);
    } finally {
      await session.cleanup();
    }
  });

  it("answers 401 when unauthenticated", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "env-unauthed");

      expect((await listProjectEnv(listRequest(new Headers()), created.id)).status).toBe(401);
      expect(
        (await putProjectEnvValue(putRequest(new Headers(), "hello"), created.id, "GREETING"))
          .status,
      ).toBe(401);
      expect(
        (await deleteProjectEnvValue(deleteRequest(new Headers()), created.id, "GREETING")).status,
      ).toBe(401);
      expect(
        (await getProjectEnvValue(getRequest(new Headers()), created.id, "GREETING")).status,
      ).toBe(401);
    } finally {
      await session.cleanup();
    }
  });

  it("authenticates before it reads the key, so a stranger learns nothing from the name", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "env-authn-first");

      for (const verb of [getProjectEnvValue, putProjectEnvValue, deleteProjectEnvValue]) {
        const response = await verb(putRequest(new Headers(), "hello"), created.id, "not a key");
        expect(response.status).toBe(401);
      }
    } finally {
      await session.cleanup();
    }
  });

  it("answers 404 for a project outside the caller's org", async () => {
    const session = await createTestSessionWithOrganization();
    const otherSession = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "env-not-yours");

      expect((await listProjectEnv(listRequest(otherSession.headers), created.id)).status).toBe(
        404,
      );
      expect(
        (
          await putProjectEnvValue(
            putRequest(otherSession.headers, "hello"),
            created.id,
            "GREETING",
          )
        ).status,
      ).toBe(404);
      expect(
        (await deleteProjectEnvValue(deleteRequest(otherSession.headers), created.id, "GREETING"))
          .status,
      ).toBe(404);
      expect(
        (await getProjectEnvValue(getRequest(otherSession.headers), created.id, "GREETING")).status,
      ).toBe(404);
      expect((await listProjectEnv(listRequest(session.headers), OTHER_ORG_PROJECT)).status).toBe(
        404,
      );
    } finally {
      await session.cleanup();
      await otherSession.cleanup();
    }
  });
});
