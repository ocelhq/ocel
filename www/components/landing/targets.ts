import type { Line } from "@/components/landing/transcript";

// The CLI section's target switcher. Each config is the provider's own docs example
// (content/docs/providers/*.mdx), with the slug the rest of the page uses. Ocel Cloud
// needs none: with no config, a logged-in deploy runs as a project named after its
// directory (resolveUnconfigured in cli/internal/commands/target.go), so its file is
// shown commented out. The output lines are the headlines the CLI prints:
// `Deployed <slug> to prod` on a provider (cli/internal/deploy/deploy.go), and the app's
// URLs then `Deployed <slug> on Ocel Cloud`, shown without the job link the CLI appends
// (cli/internal/commands/cloudrun.go).

export type TargetId = "cloud" | "aws" | "gcp" | "vps";

export type Target = { id: TargetId; label: string; config: string; output: Line[] };

const deployed = (url: string): Line[] => [
  [
    ["prompt", "$ "],
    ["cmd", "ocel deploy"],
  ],
  [],
  [
    ["ok", "✓ "],
    ["cmd", "Deployed notes to prod"],
  ],
  [
    ["muted", "  web  "],
    ["url", url],
  ],
];

const config = (provider?: { from: string; name: string; call: string }) =>
  provider
    ? `import { defineConfig } from "ocel/config";
import ${provider.name} from "${provider.from}";

export default defineConfig({
  slug: "notes",
  provider: ${provider.call},
  apps: [{ name: "web", path: "." }],
});`
    : `// Ocel Cloud works without any config. With no config file,
// ocel deploy names the project after its directory.

// import { defineConfig } from "ocel/config";
//
// export default defineConfig({
//   slug: "notes",
//   apps: [{ name: "web", path: "." }],
// });`;

export const targets: Target[] = [
  {
    id: "aws",
    label: "AWS",
    config: config({
      from: "ocel/providers/aws",
      name: "awsProvider",
      call: `awsProvider({ region: "eu-west-1" })`,
    }),
    output: deployed("https://notes.example.com"),
  },
  {
    id: "gcp",
    label: "Google Cloud",
    config: config({
      from: "ocel/providers/gcp",
      name: "gcpProvider",
      call: `gcpProvider({ project: "my-project", region: "europe-west1" })`,
    }),
    output: deployed("https://notes.example.com"),
  },
  {
    id: "vps",
    label: "VPS",
    config: config({
      from: "ocel/providers/vps",
      name: "vpsProvider",
      call: `vpsProvider({ ssh: "my-vps" })`,
    }),
    output: deployed("https://notes.example.com"),
  },
  {
    id: "cloud",
    label: "Ocel Cloud",
    config: config(),
    output: [
      [
        ["prompt", "$ "],
        ["cmd", "ocel deploy"],
      ],
      [],
      [["url", "https://notes.ocel.app"]],
      [
        ["ok", "✓ "],
        ["cmd", "Deployed notes on Ocel Cloud"],
      ],
    ],
  },
];
