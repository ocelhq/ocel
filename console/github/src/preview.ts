import type { DeployResult, Phase, Report } from "./report";

export const MARKER = "<!-- ocel-preview -->";

export type DeploymentState = "in_progress" | "success" | "failure" | "inactive";

export interface PreviewDeployment {
  id: number;
  environment: string;
  sha: string;
}

export interface PreviewComment {
  id: number;
  body: string;
  fromApp: boolean;
}

export interface DeploymentStatusInput {
  deploymentId: number;
  state: DeploymentState;
  logUrl?: string;
  environmentUrl?: string;
  autoInactive?: boolean;
}

export interface PreviewClient {
  listDeployments(input: { environment: string; sha?: string }): Promise<PreviewDeployment[]>;
  createDeployment(input: {
    ref: string;
    environment: string;
    description: string;
  }): Promise<PreviewDeployment>;
  createDeploymentStatus(input: DeploymentStatusInput): Promise<void>;
  listComments(input: { pr: number }): Promise<PreviewComment[]>;
  createComment(input: { pr: number; body: string }): Promise<void>;
  updateComment(input: { commentId: number; body: string }): Promise<void>;
}

export interface PreviewInput {
  repo: string;
  pr: number;
  sha: string;
  ref: string;
  runUrl?: string;
  phase: Phase;
  result?: DeployResult;
  error?: string;
}

export function environmentName(pr: number): string {
  return `preview/pr-${pr}`;
}

export function fromReport(report: Report): PreviewInput {
  return {
    repo: report.repo,
    pr: report.pr,
    sha: report.sha,
    ref: report.ref,
    runUrl: report.run_url,
    phase: report.phase,
    result: report.result,
    error: report.error,
  };
}

export async function reconcile(client: PreviewClient, input: PreviewInput): Promise<void> {
  const environment = environmentName(input.pr);

  if (input.phase === "removed") {
    const deployments = await client.listDeployments({ environment });
    for (const deployment of deployments) {
      await client.createDeploymentStatus({ deploymentId: deployment.id, state: "inactive" });
    }
  } else {
    const deployment = await ensureDeployment(client, environment, input.sha);
    await client.createDeploymentStatus(statusFor(input, deployment.id));
  }

  await upsertComment(client, input);
}

async function ensureDeployment(
  client: PreviewClient,
  environment: string,
  sha: string,
): Promise<PreviewDeployment> {
  const existing = await client.listDeployments({ environment, sha });
  const match = existing.find((deployment) => deployment.sha === sha);
  if (match) return match;

  return client.createDeployment({ ref: sha, environment, description: "ocel preview" });
}

function statusFor(input: PreviewInput, deploymentId: number): DeploymentStatusInput {
  if (input.phase === "deployed") {
    return {
      deploymentId,
      state: "success",
      logUrl: input.runUrl,
      environmentUrl: firstUrl(input.result),
      autoInactive: true,
    };
  }
  if (input.phase === "failed") {
    return { deploymentId, state: "failure", logUrl: input.runUrl };
  }
  return { deploymentId, state: "in_progress", logUrl: input.runUrl };
}

async function upsertComment(client: PreviewClient, input: PreviewInput): Promise<void> {
  const body = renderComment(input.phase, input);
  const comments = await client.listComments({ pr: input.pr });
  const sticky = comments.find((comment) => comment.fromApp && comment.body.includes(MARKER));
  if (sticky) {
    await client.updateComment({ commentId: sticky.id, body });
    return;
  }
  await client.createComment({ pr: input.pr, body });
}

export function firstUrl(result: DeployResult | undefined): string | undefined {
  for (const app of result?.apps ?? []) {
    const url = app.urls[0];
    if (url) return url;
  }
  return undefined;
}

const LOGO_BASE = "https://ocel.dev/providers";

interface ProviderLook {
  name: string;
  logo: string;
  home: string;
}

const PROVIDERS: Record<string, ProviderLook> = {
  "@ocel/provider-aws": { name: "AWS", logo: "aws", home: "your AWS account" },
  "@ocel/provider-vps": { name: "VPS", logo: "vps", home: "your own server" },
};

export function renderComment(phase: Phase, ctx: PreviewInput): string {
  const lines = [MARKER, "", "### Ocel preview", ""];

  if (phase === "started") {
    lines.push(`🔄 Deploying ${commitLink(ctx)} on \`${ctx.ref}\`…`);
  } else if (phase === "deployed" && ctx.result) {
    lines.push(deployedHeadline(ctx, ctx.result), "");
    lines.push("| App | Preview | Status |", "| --- | --- | --- |");
    for (const app of ctx.result.apps) {
      const url = app.urls[0];
      const preview = url ? `[Visit preview ↗](${url})` : "no url";
      lines.push(`| ${app.name} | ${preview} | ${url ? "✅ Ready" : "❌ No URL"} |`);
    }
  } else if (phase === "failed") {
    lines.push(`❌ Preview failed for ${commitLink(ctx)} on \`${ctx.ref}\`.`);
    if (ctx.error) lines.push("", "```", ctx.error, "```");
  } else {
    lines.push(`💤 Preview torn down for \`${ctx.ref}\`.`);
  }

  if (ctx.runUrl) lines.push("", `[Run log](${ctx.runUrl})`);

  return `${lines.join("\n")}\n`;
}

function deployedHeadline(ctx: PreviewInput, result: DeployResult): string {
  const look = PROVIDERS[result.provider.package];
  const where = [look?.home ?? result.provider.package, result.provider.region]
    .filter(Boolean)
    .join(" · ");
  const line = `Deployed ${commitLink(ctx)} to ${where} · ${formatTime(result.deployedAt)}`;
  if (!look) return line;
  return `<img src="${LOGO_BASE}/${look.logo}.svg" alt="${look.name}" height="14"> ${line}`;
}

export function formatTime(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  const text = new Intl.DateTimeFormat("en-US", {
    timeZone: "UTC",
    month: "short",
    day: "numeric",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(date);
  return `${text} UTC`;
}

function commitLink(ctx: PreviewInput): string {
  return `[\`${ctx.sha.slice(0, 7)}\`](https://github.com/${ctx.repo}/commit/${ctx.sha})`;
}
