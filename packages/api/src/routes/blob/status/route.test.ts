import { db } from "@repo/db";
import { uploadSession } from "@repo/db/schema";
import { eq } from "drizzle-orm";
import { beforeAll, describe, expect, it } from "vitest";
import { createTestSessionWithOrganization } from "../../../../test/auth-harness";
import { setupTestDatabase } from "../../../../test/db";
import { createProject } from "../../projects/route";
import { presignUpload } from "../presign/route";
import type { SessionFile } from "../session";
import { uploadStatus } from "./route";

const encodedMetadata = Buffer.from(
  JSON.stringify({ uploader: "avatar", metadata: {} }),
).toString("base64");

async function seedSession(
  session: { headers: Headers },
  slug: string,
): Promise<string> {
  const created = await createProject(
    new Request("http://localhost/api/projects", {
      method: "POST",
      headers: {
        ...Object.fromEntries(session.headers),
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ name: "P", slug }),
    }),
  );
  const { id: projectId } = await created.json();

  const presignRes = await presignUpload(
    new Request("http://localhost/api/blob/presign", {
      method: "POST",
      headers: {
        ...Object.fromEntries(session.headers),
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        projectId,
        bucket: "storage",
        files: [
          { key: "a.png", name: "a.png", size: 3, mimeType: "image/png" },
          { key: "b.png", name: "b.png", size: 3, mimeType: "image/png" },
        ],
        metadata: encodedMetadata,
        callbackBaseUrl: "http://localhost:3000/api/upload",
      }),
    }),
  );
  const { sessionId } = await presignRes.json();
  return sessionId;
}

function statusRequest(sessionId: string | null, headers: Headers) {
  const url = sessionId
    ? `http://localhost/api/blob/status?sessionId=${sessionId}`
    : "http://localhost/api/blob/status";
  return new Request(url, { headers: new Headers(headers) });
}

async function setFileStates(sessionId: string, states: SessionFile["state"][]) {
  const [row] = await db
    .select()
    .from(uploadSession)
    .where(eq(uploadSession.id, sessionId));
  const files = (row.files as SessionFile[]).map((f, i) => ({
    ...f,
    state: states[i],
  }));
  await db
    .update(uploadSession)
    .set({ files })
    .where(eq(uploadSession.id, sessionId));
}

describe("GET /api/blob/status", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("aggregates per-file states: pending until all succeed, then succeeded", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const sessionId = await seedSession(session, "status-agg");

      const pending = await uploadStatus(
        statusRequest(sessionId, session.headers),
      );
      expect((await pending.json()).state).toBe("pending");

      // One of two files done -> still pending.
      await setFileStates(sessionId, ["succeeded", "pending"]);
      expect(
        (await (await uploadStatus(statusRequest(sessionId, session.headers))).json())
          .state,
      ).toBe("pending");

      // Both done -> succeeded.
      await setFileStates(sessionId, ["succeeded", "succeeded"]);
      expect(
        (await (await uploadStatus(statusRequest(sessionId, session.headers))).json())
          .state,
      ).toBe("succeeded");
    } finally {
      await session.cleanup();
    }
  });

  it("reports expired when any file is expired", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const sessionId = await seedSession(session, "status-expired");
      await setFileStates(sessionId, ["succeeded", "expired"]);
      const res = await uploadStatus(statusRequest(sessionId, session.headers));
      expect((await res.json()).state).toBe("expired");
    } finally {
      await session.cleanup();
    }
  });

  it("404s a session in an org the caller doesn't belong to (no cross-tenant leak)", async () => {
    const owner = await createTestSessionWithOrganization();
    const other = await createTestSessionWithOrganization();
    try {
      const sessionId = await seedSession(owner, "status-cross-tenant");
      const res = await uploadStatus(statusRequest(sessionId, other.headers));
      expect(res.status).toBe(404);
    } finally {
      await owner.cleanup();
      await other.cleanup();
    }
  });

  it("404s an unknown session", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const res = await uploadStatus(statusRequest("nope", session.headers));
      expect(res.status).toBe(404);
    } finally {
      await session.cleanup();
    }
  });

  it("400s a missing sessionId", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const res = await uploadStatus(statusRequest(null, session.headers));
      expect(res.status).toBe(400);
    } finally {
      await session.cleanup();
    }
  });

  it("401s when unauthenticated", async () => {
    const res = await uploadStatus(statusRequest("s", new Headers()));
    expect(res.status).toBe(401);
  });
});
