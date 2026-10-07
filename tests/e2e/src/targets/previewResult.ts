import type { PreviewRelease } from "./types";

type SummaryApp = { app?: string; urls?: string[]; deploymentUrl?: string };

type Summary = { apps?: SummaryApp[] };

type DeployResultRecord = { apps?: { name?: string; buildId?: string }[] };

function summaryIn(stream: string): Summary | undefined {
  let found: Summary | undefined;
  for (const line of stream.split("\n")) {
    try {
      const parsed = JSON.parse(line) as { summary?: Summary } | null;
      if (parsed !== null && typeof parsed === "object" && "summary" in parsed) {
        found = parsed.summary;
      }
    } catch {}
  }
  return found;
}

export function previewReleasesIn(
  stream: string,
  record: string,
  command: string,
): PreviewRelease[] {
  const apps = summaryIn(stream)?.apps;
  if (apps === undefined || apps.length === 0) {
    throw new Error(`\`${command}\` streamed no summary naming its apps`);
  }
  const recorded = (JSON.parse(record) as DeployResultRecord).apps ?? [];
  return apps.map((one) => {
    const app = one.app ?? "";
    if (!one.deploymentUrl) {
      throw new Error(
        `\`${command}\` published no deployment url for ${app}, so its deployments cannot be told apart`,
      );
    }
    const buildId = recorded.find((each) => each.name === app)?.buildId;
    if (!buildId) {
      throw new Error(`the record in .ocel/deploy-result.json holds no build id for ${app}`);
    }
    return { app, urls: one.urls ?? [], deploymentUrl: one.deploymentUrl, buildId };
  });
}
