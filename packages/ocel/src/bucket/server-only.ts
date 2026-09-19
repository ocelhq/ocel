const host = globalThis as { window?: { document?: unknown } };

if (host.window !== undefined && host.window.document !== undefined) {
  throw new Error(
    "ocel/bucket/next reaches this deployment's runtime with its session token, so it may only be imported by server code. Import ocel/bucket/client from a Client Component instead.",
  );
}

export {};
