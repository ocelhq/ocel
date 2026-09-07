import { createNodeMiddleware, createProbot } from "probot";
import { env } from "../../../ocel/vars";
import { app as githubApp } from "./app";
import { clientFor } from "./github";
import { createApp, privateKey } from "./http";
import { fromReport, reconcile } from "./preview";

const WEBHOOKS_PATH = "/api/github/webhooks";
const PORT = Number(process.env.PORT ?? 3000);

const probot = createProbot({
  overrides: {
    appId: Number(env.APP_ID),
    privateKey: privateKey(env.PRIVATE_KEY),
    secret: env.WEBHOOK_SECRET,
  },
});

const middleware = await createNodeMiddleware(githubApp, { probot, webhooksPath: WEBHOOKS_PATH });

const app = createApp({
  reportSecret: env.REPORT_SECRET,
  onReport: async (report) => {
    const client = await clientFor(probot, report.repo);
    await reconcile(client, fromReport(report));
  },
  webhooks: (req, res, next) => {
    void middleware(req, res, next);
  },
});

app.listen(PORT, () => {
  console.log(`ocel github app listening on http://localhost:${PORT}`);
});
