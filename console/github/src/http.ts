import { createHmac, timingSafeEqual } from "node:crypto";
import express, { type RequestHandler } from "express";
import { type Report, reportSchema } from "./report";

const SIGNATURE_HEADER = "x-ocel-signature-256";

export interface AppOptions {
  reportSecret: string;
  onReport(report: Report): Promise<void>;
  webhooks?: RequestHandler;
}

export function createApp(options: AppOptions): express.Express {
  const app = express();

  if (options.webhooks) app.use(options.webhooks);

  app.get("/healthz", (_req, res) => {
    res.json({ ok: true });
  });

  app.post("/api/preview", express.raw({ type: "*/*", limit: "1mb" }), async (req, res) => {
    const raw = Buffer.isBuffer(req.body) ? req.body : Buffer.alloc(0);
    if (!verifySignature(raw, req.header(SIGNATURE_HEADER), options.reportSecret)) {
      res.status(401).json({ error: "bad signature" });
      return;
    }

    let payload: unknown;
    try {
      payload = JSON.parse(raw.toString("utf8"));
    } catch {
      res.status(400).json({ error: "invalid json" });
      return;
    }

    const parsed = reportSchema.safeParse(payload);
    if (!parsed.success) {
      res.status(400).json({ error: "invalid report" });
      return;
    }

    try {
      await options.onReport(parsed.data);
    } catch (error) {
      console.error(error);
      res.status(502).json({ error: "github request failed" });
      return;
    }

    res.json({ ok: true });
  });

  return app;
}

export function verifySignature(
  body: Buffer,
  signature: string | undefined,
  secret: string,
): boolean {
  if (!signature) return false;
  const expected = Buffer.from(
    `sha256=${createHmac("sha256", secret).update(body).digest("hex")}`,
    "utf8",
  );
  const given = Buffer.from(signature, "utf8");
  return expected.length === given.length && timingSafeEqual(expected, given);
}

export function privateKey(raw: string): string {
  return raw.replace(/\\n/g, "\n");
}
