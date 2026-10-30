import posthog from "posthog-js";
import { INGEST_PATH, posthogHosts } from "@/lib/posthog";

const projectToken = process.env.NEXT_PUBLIC_POSTHOG_PROJECT_TOKEN;
const apiHost = process.env.NEXT_PUBLIC_POSTHOG_HOST;

if (projectToken && apiHost) {
  posthog.init(projectToken, {
    api_host: INGEST_PATH,
    ui_host: posthogHosts(apiHost).ui,
    defaults: "2026-01-30",
    capture_exceptions: true,
    capture_heatmaps: true,
    capture_performance: { web_vitals: true },
    debug: process.env.NODE_ENV === "development",
  });
}
