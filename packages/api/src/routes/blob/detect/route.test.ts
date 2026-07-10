import { db } from "@repo/db";
import { uploadSession } from "@repo/db/schema";
import { eq } from "drizzle-orm";
import { beforeAll, describe, expect, it } from "vitest";
import { createTestSessionWithOrganization } from "../../../../test/auth-harness";
import { setupTestDatabase } from "../../../../test/db";
import { createProject } from "../../projects/route";
import { presignUpload } from "../presign/route";
import type { SessionFile } from "../session";
import { verifyUpload } from "../signing";
import { detectUploads } from "./route";

const encodedMetadata = Buffer.from(
  JSON.stringify({ uploader: "avatar", metadata: {} }),
).toString("base64");

async function createProjectFor(session: { headers: Headers }, slug: string) {
  const res = await createProject(
    new Request("http://localhost/api/projects", {
      method: "POST",
      headers: {
        ...Object.fromEntries(session.headers),
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ name: "P", slug }),
    }),
  );
  return res.json() as Promise<{ id: string }>;
}

async function presign(
  session: { headers: Headers },
  projectId: string,
): Promise<{ sessionId: string; url: string; key: string }> {
  const res = await presignUpload(
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
          { key: "photo.png", name: "photo.png", size: 5, mimeType: "image/png" },
        ],
        metadata: encodedMetadata,
        callbackBaseUrl: "http://localhost:3000/api/upload",
      }),
    }),
  );
  const body = await res.json();
  return { sessionId: body.sessionId, url: body.files[0].url, key: body.files[0].key };
}

function detectRequest(projectId: string, headers: Headers) {
  return new Request("http://localhost/api/blob/detect", {
    method: "POST",
    headers: {
      ...Object.fromEntries(headers),
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ projectId }),
  });
}

async function fileState(sessionId: string, idx: number) {
  const [row] = await db
    .select()
    .from(uploadSession)
    .where(eq(uploadSession.id, sessionId));
  return (row.files as SessionFile[])[idx];
}

// This suite drives the real MinIO from docker-compose (store.ts's OCEL_BLOB_*
// defaults). Run `docker compose up -d minio minio-createbucket` first.
describe("POST /api/blob/detect (MinIO)", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("no-ops before the object lands, then transitions + signs once and is idempotent on a second sweep", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const { id: projectId } = await createProjectFor(session, "detect-once");
      const { sessionId, url, key } = await presign(session, projectId);

      // Before the bytes land: sweep finds nothing, file stays pending.
      const before = await detectUploads(detectRequest(projectId, session.headers));
      expect((await before.json()).completions).toHaveLength(0);
      expect((await fileState(sessionId, 0)).state).toBe("pending");

      // Real browser-style PUT to the presigned MinIO URL.
      const put = await fetch(url, {
        method: "PUT",
        body: Buffer.from("bytes"),
        headers: { "content-type": "image/png" },
      });
      expect(put.status).toBeLessThan(300);

      // First sweep after landing: exactly one completion, correctly signed,
      // and the file is now succeeded.
      const first = await detectUploads(detectRequest(projectId, session.headers));
      const firstBody = await first.json();
      expect(firstBody.completions).toHaveLength(1);
      const completion = firstBody.completions[0];
      expect(completion.sessionId).toBe(sessionId);
      expect(completion.file.key).toBe(key);
      expect(completion.callbackBaseUrl).toBe("http://localhost:3000/api/upload");

      const [row] = await db
        .select()
        .from(uploadSession)
        .where(eq(uploadSession.id, sessionId));
      expect(
        verifyUpload(row.secret, sessionId, completion.file, completion.signature),
      ).toBe(true);
      expect((await fileState(sessionId, 0)).state).toBe("succeeded");

      // Second sweep: the object still exists, but the guarded transition
      // no-ops (already succeeded) so no duplicate completion is emitted -
      // onUploadComplete would run exactly once.
      const second = await detectUploads(detectRequest(projectId, session.headers));
      expect((await second.json()).completions).toHaveLength(0);
    } finally {
      await session.cleanup();
    }
  });

  it("emits no duplicate under overlapping concurrent sweeps", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const { id: projectId } = await createProjectFor(session, "detect-concurrent");
      const { sessionId, url } = await presign(session, projectId);
      await fetch(url, {
        method: "PUT",
        body: Buffer.from("bytes"),
        headers: { "content-type": "image/png" },
      });

      // Fire several sweeps at once; the atomic conditional transition must let
      // exactly one observe the pending->succeeded edge.
      const results = await Promise.all(
        Array.from({ length: 5 }, () =>
          detectUploads(detectRequest(projectId, session.headers)).then((r) =>
            r.json(),
          ),
        ),
      );
      const total = results.reduce((n, r) => n + r.completions.length, 0);
      expect(total).toBe(1);
      expect((await fileState(sessionId, 0)).state).toBe("succeeded");
    } finally {
      await session.cleanup();
    }
  });

  it("does not sweep another user's session in the same project", async () => {
    const owner = await createTestSessionWithOrganization();
    const other = await createTestSessionWithOrganization();
    try {
      const { id: projectId } = await createProjectFor(owner, "detect-scoped");
      const { url } = await presign(owner, projectId);
      await fetch(url, {
        method: "PUT",
        body: Buffer.from("bytes"),
        headers: { "content-type": "image/png" },
      });
      // `other` is not a member of the project's org -> 404, never sees the
      // owner's landed object.
      const res = await detectUploads(detectRequest(projectId, other.headers));
      expect(res.status).toBe(404);
    } finally {
      await owner.cleanup();
      await other.cleanup();
    }
  });

  it("401s when unauthenticated", async () => {
    const res = await detectUploads(detectRequest("p", new Headers()));
    expect(res.status).toBe(401);
  });
});
