// TODO: abridged from cli/internal/terminal/testdata/streams; replace with captured runs.

export type TargetId = "cloud" | "aws" | "gcp" | "vps";

export type Tone = "prompt" | "cmd" | "muted" | "ok" | "bold" | "url" | "warn" | "plain";
export type Segment = readonly [Tone, string];
export type Line = readonly Segment[];

export type Target = {
  id: TargetId;
  name: string;
  status?: string;
  needs: string;
  why: string;
  config: string;
};

export const targets: readonly Target[] = [
  {
    id: "cloud",
    name: "Ocel Cloud",
    status: "early access",
    needs: "An early-access invite",
    why: "the app runs on Ocel's machines, so there is no account of yours to reach",
    config: "",
  },
  {
    id: "aws",
    name: "AWS",
    needs: "AWS credentials in your shell",
    why: "the app runs in your account, and Ocel provisions with them",
    config: `"provider": "aws",`,
  },
  {
    id: "gcp",
    name: "Google Cloud",
    needs: "gcloud application-default credentials",
    why: "the app runs in your project, and Ocel provisions with them",
    config: `"provider": "gcp",`,
  },
  {
    id: "vps",
    name: "VPS",
    needs: "A Linux box you can reach over SSH",
    why: "the app runs there, and Ocel installs it over that connection",
    config: `"provider": { "vps": { "ssh": "my-box" } },`,
  },
];

const p = (cmd: string): Line => [
  ["prompt", "$ "],
  ["cmd", cmd],
];
const blank: Line = [];
const head = (tier: string): Line => [
  ["bold", "ocel"],
  ["muted", "  0.0.2  "],
  ["bold", "notes"],
  ["muted", " › "],
  ["warn", tier],
];

export const deploy: Record<TargetId, readonly Line[]> = {
  cloud: [
    p("ocel login"),
    [
      ["ok", "✓ "],
      ["plain", "Logged in as ada@example.com"],
    ],
    [["muted", "  https://app.ocelhq.com offers Ocel Cloud."]],
    blank,
    p("ocel deploy"),
    head("production"),
    blank,
    [
      ["muted", "  "],
      ["bold", "Ocel Cloud"],
      ["muted", "  ada-example · as ada@example.com"],
    ],
    blank,
    [
      ["muted", "INFO  [build] "],
      ["ok", "✓ "],
      ["plain", "notes: Uploaded the source in 2s"],
    ],
    [
      ["muted", "INFO  [deploy] "],
      ["ok", "✓ "],
      ["plain", "notes: Started the job on Ocel Cloud"],
    ],
    [
      ["muted", "INFO  [deploy] "],
      ["plain", "Job 7kq2m4 · follow it at "],
      ["url", "app.ocelhq.com/cloud/jobs/7kq2m4"],
    ],
    [
      ["muted", "INFO  [deploy] "],
      ["ok", "✓ "],
      ["plain", "notes: Waited for the job on Ocel Cloud in 48s"],
    ],
    blank,
    [
      ["ok", "✓ "],
      ["bold", "Deployed notes on Ocel Cloud"],
    ],
    [
      ["muted", "  web  "],
      ["url", "https://notes.ocel.app"],
    ],
  ],
  aws: [
    p("ocel deploy"),
    head("production"),
    blank,
    [
      ["muted", "  "],
      ["bold", "aws"],
      ["muted", "         123456789012 · us-east-1 · as deploy"],
    ],
    [
      ["muted", "  "],
      ["bold", "cloudfront"],
      ["muted", "  E2Q4H7ZK1XWM3A"],
    ],
    blank,
    [
      ["muted", "INFO  [check] "],
      ["ok", "✓ "],
      ["plain", "aws: Checked your credentials and the production bootstrap for notes in 1s"],
    ],
    blank,
    [["plain", "Change plan, fronted by the cloudfront edge:"]],
    [
      ["ok", "+ "],
      ["plain", "notes"],
    ],
    [
      ["ok", "    + "],
      ["plain", "main  postgres"],
    ],
    [
      ["ok", "    + "],
      ["plain", "web   function"],
    ],
    [["plain", "2 to create."]],
    blank,
    [
      ["muted", "INFO  [build] "],
      ["ok", "✓ "],
      ["plain", "web: Built app web in 14s"],
    ],
    [
      ["muted", "INFO  [deploy] "],
      ["ok", "✓ "],
      ["plain", "web: Deployed the serverless app to production in 3m 2s"],
    ],
    blank,
    [
      ["ok", "✓ "],
      ["bold", "Deployed notes to production in 3m 21s"],
    ],
    [
      ["muted", "  web  "],
      ["url", "https://notes.example.com"],
    ],
  ],
  gcp: [
    p("ocel deploy"),
    head("production"),
    blank,
    [
      ["muted", "  "],
      ["bold", "gcp"],
      ["muted", "  notes-prod · us-central1 · as deploy@notes-prod.iam"],
    ],
    blank,
    [
      ["muted", "INFO  [check] "],
      ["ok", "✓ "],
      ["plain", "gcp: Checked your credentials and the production bootstrap for notes in 2s"],
    ],
    blank,
    [["plain", "Change plan:"]],
    [
      ["ok", "+ "],
      ["plain", "notes"],
    ],
    [
      ["ok", "    + "],
      ["plain", "main  postgres"],
    ],
    [
      ["ok", "    + "],
      ["plain", "web   container"],
    ],
    [["plain", "2 to create."]],
    blank,
    [
      ["muted", "INFO  [build] "],
      ["ok", "✓ "],
      ["plain", "web: Built app web in 31s"],
    ],
    [
      ["muted", "INFO  [deploy] "],
      ["ok", "✓ "],
      ["plain", "web: Deployed the container app to production in 4m 10s"],
    ],
    blank,
    [
      ["ok", "✓ "],
      ["bold", "Deployed notes to production in 4m 46s"],
    ],
    [
      ["muted", "  web  "],
      ["url", "https://notes.example.com"],
    ],
  ],
  vps: [
    p("ocel deploy"),
    head("production"),
    blank,
    [
      ["muted", "  "],
      ["bold", "vps"],
      ["muted", "  my-box · as deploy"],
    ],
    blank,
    [
      ["muted", "INFO  [check] "],
      ["ok", "✓ "],
      ["plain", "vps: Checked the box and the production bootstrap for notes in 1s"],
    ],
    blank,
    [["plain", "Change plan:"]],
    [
      ["ok", "+ "],
      ["plain", "notes"],
    ],
    [
      ["ok", "    + "],
      ["plain", "main  postgres"],
    ],
    [
      ["ok", "    + "],
      ["plain", "web   container"],
    ],
    [["plain", "2 to create."]],
    blank,
    [
      ["muted", "INFO  [build] "],
      ["ok", "✓ "],
      ["plain", "web: Built app web in 22s"],
    ],
    [
      ["muted", "INFO  [deploy] "],
      ["ok", "✓ "],
      ["plain", "web: Deployed the container app to production in 19s"],
    ],
    blank,
    [
      ["ok", "✓ "],
      ["bold", "Deployed notes to production in 48s"],
    ],
    [
      ["muted", "  web  "],
      ["url", "https://notes.example.com"],
    ],
  ],
};

const previewHead = head("preview checkout");

export const preview: Record<TargetId, readonly Line[]> = {
  cloud: [
    p("git switch -c checkout"),
    p("ocel preview up"),
    previewHead,
    blank,
    [
      ["muted", "INFO  [deploy] "],
      ["ok", "✓ "],
      ["plain", "notes: Waited for the job on Ocel Cloud in 39s"],
    ],
    blank,
    [
      ["ok", "✓ "],
      ["bold", "Deployed a preview of notes on Ocel Cloud"],
    ],
    [
      ["muted", "  web  "],
      ["url", "https://notes-k3v9x2m4q8w1z7c5a1b2c3d4.preview.ocel.app"],
    ],
  ],
  aws: [
    p("git switch -c checkout"),
    p("ocel preview up"),
    previewHead,
    blank,
    [
      ["muted", "INFO  [deploy] "],
      ["ok", "✓ "],
      ["plain", "web: Deployed the serverless app to preview checkout in 1m 4s"],
    ],
    blank,
    [
      ["ok", "✓ "],
      ["bold", "Deployed notes to preview checkout in 1m 19s"],
    ],
    [
      ["muted", "  web  "],
      ["url", "https://checkout.notes-previews.com"],
    ],
  ],
  gcp: [
    p("git switch -c checkout"),
    p("ocel preview up"),
    previewHead,
    blank,
    [
      ["muted", "INFO  [deploy] "],
      ["ok", "✓ "],
      ["plain", "web: Deployed the container app to preview checkout in 1m 52s"],
    ],
    blank,
    [
      ["ok", "✓ "],
      ["bold", "Deployed notes to preview checkout in 2m 20s"],
    ],
    [
      ["muted", "  web  "],
      ["url", "https://checkout.notes-previews.com"],
    ],
  ],
  vps: [
    p("git switch -c checkout"),
    p("ocel preview up"),
    previewHead,
    blank,
    [
      ["muted", "INFO  [deploy] "],
      ["ok", "✓ "],
      ["plain", "web: Deployed the container app to preview checkout in 14s"],
    ],
    blank,
    [
      ["ok", "✓ "],
      ["bold", "Deployed notes to preview checkout in 31s"],
    ],
    [
      ["muted", "  web  "],
      ["url", "https://checkout.notes-previews.com"],
    ],
  ],
};

export const dev: readonly Line[] = [
  p("ocel dev -- next dev"),
  blank,
  [
    ["bold", 'postgres "main"'],
    ["muted", " → postgres:17 @ 127.0.0.1:54312"],
  ],
  [
    ["bold", 'bucket "uploads"'],
    ["muted", " → 127.0.0.1:54313"],
  ],
  blank,
  [["plain", "  ▲ Next.js 16.2.10"]],
  [
    ["muted", "  - Local: "],
    ["url", "http://localhost:3000"],
  ],
];

export const agent: readonly Line[] = [
  p("ocel skill install --yes"),
  [
    ["ok", "✓ "],
    ["plain", "Wrote .claude/skills/ocel and .agents/skills/ocel"],
  ],
  blank,
  p("ocel schema deploy | head -c 64"),
  [["plain", '{"$schema":"https://json-schema.org/draft/2020-12/schema",…']],
  blank,
  p("ocel env ls --json"),
  [["plain", '{"ok":false,"error":{"code":"project.no_config",']],
  [["plain", '  "hint":"ocel init --provider <id>",']],
  [["plain", '  "docsUrl":"https://ocel.dev/docs/errors/project.no_config"}}']],
];
