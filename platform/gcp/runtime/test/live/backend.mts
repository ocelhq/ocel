import { execFileSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";

export interface Backend {
  project: string;
  storageEndpoint?: string;
  firestoreEndpoint?: string;
  metadataOrigin?: string;
  authorizedFetch: typeof fetch;
  tagDatabase(): string;
  close(): Promise<void>;
}

const emulatedProject = "floci-local";
const tokenPath = "/computeMetadata/v1/instance/service-accounts/default/token";

export async function openBackend(): Promise<Backend> {
  const flociEndpoint = process.env.OCEL_FLOCI_GCP_ENDPOINT?.trim();
  if (flociEndpoint) {
    return emulatedBackend(flociEndpoint);
  }
  const project = process.env.OCEL_GCP_LIVE_PROJECT?.trim();
  if (project) {
    return realBackend(project);
  }
  throw new Error(
    "no floci-gcp emulator and OCEL_GCP_LIVE_PROJECT names no project; run under scripts/floci.sh --cloud gcp run <name> -- pnpm --filter @platform/gcp-runtime live",
  );
}

function emulatedBackend(storageEndpoint: string): Backend {
  const firestoreEndpoint = process.env.OCEL_FLOCI_FIRESTORE_ENDPOINT?.trim();
  if (!firestoreEndpoint) {
    throw new Error(
      "OCEL_FLOCI_FIRESTORE_ENDPOINT is not set, and the tag records live in Google's Firestore emulator, which scripts/floci.sh --cloud gcp starts beside floci",
    );
  }
  return {
    project: emulatedProject,
    storageEndpoint,
    firestoreEndpoint,
    authorizedFetch: fetch,
    tagDatabase: () =>
      `projects/${emulatedProject}/databases/ocel-live-tags-${randomBytes(4).toString("hex")}`,
    close: async () => {},
  };
}

async function realBackend(project: string): Promise<Backend> {
  const token = execFileSync("gcloud", ["auth", "print-access-token"], {
    encoding: "utf8",
  }).trim();
  const server = createServer((request, response) => {
    if (request.method === "GET" && request.url === tokenPath) {
      response.writeHead(200, { "Content-Type": "application/json" });
      response.end(JSON.stringify({ access_token: token, expires_in: 3000, token_type: "Bearer" }));
      return;
    }
    response.writeHead(404).end();
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address() as AddressInfo;
  const namespace = process.env.OCEL_NAMESPACE?.trim() || "ocel";
  return {
    project,
    metadataOrigin: `http://127.0.0.1:${port}`,
    authorizedFetch: (input, init) => {
      const headers = new Headers(init?.headers);
      headers.set("Authorization", `Bearer ${token}`);
      return fetch(input, { ...init, headers });
    },
    tagDatabase: () => `projects/${project}/databases/${namespace}-production-tags`,
    close: () =>
      new Promise<void>((resolve, reject) => {
        server.close((error) => (error ? reject(error) : resolve()));
        server.closeAllConnections();
      }),
  };
}
