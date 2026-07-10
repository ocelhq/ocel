import { HeadObjectCommand, S3Client } from "@aws-sdk/client-s3";
import { execFileSync, spawn, type ChildProcess } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { createServer, type Server } from "node:http";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { bucket, createRouteHandler, uploader } from "ocel/blob";
import { createUploadClient } from "ocel/blob/client";
import { z } from "zod";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { createTestSessionWithOrganization } from "../../../test/auth-harness";
import { setupTestDatabase } from "../../../test/db";
import { createProject } from "../projects/route";
import { detectUploads } from "./detect/route";
import { presignUpload } from "./presign/route";
import { uploadStatus } from "./status/route";
import { verifyUploadSignature } from "./verify/route";

// The one new top-level dev seam. It drives the full ocel/blob dev completion
// path end to end with ZERO prod cloud, using the SAME SDK code prod will use:
//
//   real SDK createUploadClient.upload
//     -> SDK route ?op=presign -> [Connect] real Go dev server (blobrig)
//        -> Ocel API /api/blob/presign -> real MinIO presigned URL
//     -> real browser-style PUT of bytes to MinIO
//     -> the Go dev server's detection loop -> Ocel API /api/blob/detect
//        (HEADs MinIO, atomic pending->succeeded, signs) -> signed ?op=callback
//     -> SDK route ?op=callback -> [Connect] dev server -> API /api/blob/verify
//        -> onUploadComplete fires exactly once
//     -> SDK client ?op=poll -> ... -> API /api/blob/status -> succeeded
//        -> onClientUploadComplete
//
// It needs the Go toolchain (to build the dev-server rig) and MinIO from
// docker-compose; it self-skips when either is absent, so it stays runnable
// locally (`docker compose up -d minio minio-createbucket` then `pnpm test`)
// without breaking environments that lack them - mirroring how the awslive
// deploy e2e gates on a real account.

const here = path.dirname(fileURLToPath(import.meta.url));
// src/routes/blob -> repo root -> cli
const cliDir = path.resolve(here, "..", "..", "..", "..", "..", "cli");

function goAvailable(): boolean {
  try {
    execFileSync("go", ["version"], { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

async function minioReachable(): Promise<boolean> {
  const endpoint = process.env.OCEL_BLOB_ENDPOINT ?? "http://localhost:9000";
  try {
    const res = await fetch(`${endpoint}/minio/health/live`);
    return res.ok;
  } catch {
    return false;
  }
}

async function toWebRequest(
  // biome-ignore lint/suspicious/noExplicitAny: node req
  req: any,
  base: string,
): Promise<Request> {
  const headers = new Headers();
  for (const [k, v] of Object.entries(req.headers)) {
    if (typeof v === "string") headers.set(k, v);
    else if (Array.isArray(v)) headers.set(k, v.join(","));
  }
  let body: Buffer | undefined;
  if (req.method !== "GET" && req.method !== "HEAD") {
    const chunks: Buffer[] = [];
    for await (const c of req) chunks.push(c as Buffer);
    body = Buffer.concat(chunks);
  }
  return new Request(base + req.url, {
    method: req.method,
    headers,
    body: body && body.length > 0 ? body : undefined,
  });
}

// biome-ignore lint/suspicious/noExplicitAny: node res
async function sendWebResponse(res: any, webRes: Response): Promise<void> {
  res.statusCode = webRes.status;
  webRes.headers.forEach((v, k) => res.setHeader(k, v));
  res.end(Buffer.from(await webRes.arrayBuffer()));
}

async function waitForRigAddr(child: ChildProcess): Promise<string> {
  return new Promise((resolve, reject) => {
    let buf = "";
    const onData = (d: Buffer) => {
      buf += d.toString();
      const m = buf.match(/RIG_ADDR=(\S+)/);
      if (m) {
        child.stdout?.off("data", onData);
        resolve(m[1]);
      }
    };
    child.stdout?.on("data", onData);
    child.stderr?.on("data", (d) => process.stderr.write(`[blobrig] ${d}`));
    child.on("exit", (code) =>
      reject(new Error(`blobrig exited early (code ${code})`)),
    );
    setTimeout(() => reject(new Error("blobrig never printed RIG_ADDR")), 15_000);
  });
}

const runIt = (await minioReachable()) && goAvailable() ? it : it.skip;

describe("ocel/blob dev e2e (MinIO)", () => {
  let session: Awaited<ReturnType<typeof createTestSessionWithOrganization>>;
  let projectId: string;
  let rigBin: string;
  let rigDir: string;
  let rig: ChildProcess | undefined;
  let server: Server | undefined;
  let appBase = "";

  beforeAll(async () => {
    if (!(await minioReachable()) || !goAvailable()) return;
    await setupTestDatabase();

    session = await createTestSessionWithOrganization();
    const created = await createProject(
      new Request("http://localhost/api/projects", {
        method: "POST",
        headers: {
          ...Object.fromEntries(session.headers),
          "Content-Type": "application/json",
        },
        body: JSON.stringify({ name: "Blob E2E", slug: "blob-dev-e2e" }),
      }),
    );
    projectId = (await created.json()).id;

    rigDir = mkdtempSync(path.join(tmpdir(), "blobrig-"));
    rigBin = path.join(rigDir, "blobrig");
    execFileSync("go", ["build", "-o", rigBin, "./internal/blobrig"], {
      cwd: cliDir,
      stdio: "inherit",
    });
  }, 120_000);

  afterAll(async () => {
    rig?.kill("SIGTERM");
    await new Promise((r) => server?.close(r) ?? r(undefined));
    if (session) await session.cleanup();
    if (rigDir) rmSync(rigDir, { recursive: true, force: true });
  });

  runIt(
    "presign -> real PUT to MinIO -> detection -> onUploadComplete once -> poll -> onClientUploadComplete",
    async () => {
      const token = session.token;

      // The completion signals the assertions hinge on: the SERVER-decided
      // onUploadComplete (fired by the detector's callback) and the client's
      // poll-driven onClientUploadComplete.
      const completedPaths: string[] = [];
      const storageBucket = bucket("storage", {
        uploaders: {
          avatar: uploader(
            {
              input: z.object({ userId: z.string() }),
              middleware: async ({ input }) => ({ userId: input.userId }),
            },
            {
              accept: ["image/png"],
              path: { prefix: "avatars/" },
              contentDisposition: "inline",
              onUploadComplete: async ({ file }) => {
                completedPaths.push(file.path);
              },
            },
          ),
        },
      });

      // Start the REAL Go dev server (Connect RuntimeService shim + detector).
      // Its Ocel API base URL is this test's in-process HTTP server, reached
      // once we know its port; bind the server first to learn the port.
      const listened = await new Promise<{ srv: Server; port: number }>(
        (resolve) => {
          const srv = createServer();
          srv.listen(0, "127.0.0.1", () =>
            resolve({ srv, port: (srv.address() as { port: number }).port }),
          );
        },
      );
      server = listened.srv;
      appBase = `http://127.0.0.1:${listened.port}`;

      rig = spawn(
        rigBin,
        ["-api", appBase, "-token", token, "-project", projectId],
        { stdio: ["pipe", "pipe", "pipe"] },
      );
      const rigAddr = await waitForRigAddr(rig);

      // Inject the resolved bucket env exactly as `ocel dev` does, so the SDK
      // route resolves its runtime context (Connect dial to the dev server)
      // through the same code path prod uses.
      process.env.OCEL_RESOURCE_BUCKET_storage = JSON.stringify({
        address: rigAddr,
        bucket: "storage",
      });

      const route = createRouteHandler(storageBucket);

      // Mount the Ocel API endpoints (reached by the dev server) and the SDK
      // app route (reached by the client and by the detector's callback) on the
      // one in-process server.
      server.on("request", (req, res) => {
        (async () => {
          const url = new URL(appBase + req.url);
          const webReq = await toWebRequest(req, appBase);
          if (url.pathname === "/api/blob/presign") {
            return sendWebResponse(res, await presignUpload(webReq));
          }
          if (url.pathname === "/api/blob/verify") {
            return sendWebResponse(res, await verifyUploadSignature(webReq));
          }
          if (url.pathname === "/api/blob/status") {
            return sendWebResponse(res, await uploadStatus(webReq));
          }
          if (url.pathname === "/api/blob/detect") {
            return sendWebResponse(res, await detectUploads(webReq));
          }
          if (url.pathname === "/app/upload") {
            const handler = req.method === "GET" ? route.GET : route.POST;
            // biome-ignore lint/suspicious/noExplicitAny: BlobRequest is a web Request
            return sendWebResponse(res, await handler(webReq as any));
          }
          res.statusCode = 404;
          res.end();
        })().catch((err) => {
          res.statusCode = 500;
          res.end(String(err));
        });
      });

      const client = createUploadClient<typeof storageBucket>({
        url: `${appBase}/app/upload`,
        pollIntervalMs: 200,
        maxPollMs: 20_000,
      });

      // A real File (Blob) so the SDK client PUTs actual bytes to MinIO, with
      // content-type/length matching the signed presigned URL - exactly a
      // browser upload.
      const file = new File([Buffer.from("bytes")], "me.png", {
        type: "image/png",
      });

      const clientCompleted: string[] = [];
      const result = await client.upload(
        "avatar",
        {
          files: [file],
          input: { userId: "u-42" },
        },
        {
          onClientUploadComplete: ({ files }) => {
            clientCompleted.push(...files.map((f) => f.key));
          },
        },
      );

      // The real bytes must have gone to the presigned MinIO URL: the whole
      // flow can only reach "succeeded" if the detector HEADed a real object.
      expect(result.files).toHaveLength(1);
      const landedKey = result.files[0].key;
      expect(landedKey).toContain(`${projectId}/`); // honest tenancy prefix
      expect(landedKey).toContain("avatars/me.png");

      // Server-authoritative completion fired exactly once, with the real path.
      expect(completedPaths).toEqual([landedKey]);
      // Client learned the server-decided outcome by polling.
      expect(clientCompleted).toEqual([landedKey]);

      // The uploader's contentDisposition is bound onto the stored object: the
      // presign signs it, the client sends it on the PUT, so MinIO persists it
      // as object metadata (US-12).
      const s3 = new S3Client({
        region: process.env.OCEL_BLOB_REGION ?? "us-east-1",
        endpoint: process.env.OCEL_BLOB_ENDPOINT ?? "http://localhost:9000",
        forcePathStyle: true,
        credentials: {
          accessKeyId: process.env.OCEL_BLOB_ACCESS_KEY_ID ?? "minioadmin",
          secretAccessKey: process.env.OCEL_BLOB_SECRET_ACCESS_KEY ?? "minioadmin",
        },
      });
      const head = await s3.send(
        new HeadObjectCommand({
          Bucket: process.env.OCEL_BLOB_BUCKET ?? "ocel-dev",
          Key: landedKey,
        }),
      );
      expect(head.ContentDisposition).toBe("inline");
    },
    60_000,
  );
});
