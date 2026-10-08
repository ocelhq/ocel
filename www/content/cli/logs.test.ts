import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const repo = resolve(__dirname, "../../..");
const read = (path: string) => readFileSync(resolve(repo, path), "utf8");

const sources = {
  output: "cli/internal/commands/logs/output.go",
  containers: "platform/vps/provider/host/containers.go",
};

const page = read("www/content/cli/logs.mdx");

function jsonFieldsOf(go: string, type: string) {
  const body = go.match(new RegExp(`type ${type} struct \\{([^}]*)\\}`))?.[1] ?? "";
  return [...body.matchAll(/json:"(\w+)/g)].map((m) => m[1]);
}

function section(heading: string) {
  const start = page.indexOf(`## ${heading}`);
  const end = page.indexOf("\n## ", start + 1);
  return page.slice(start, end === -1 ? undefined : end);
}

describe("the ocel logs prose", () => {
  it("is listed in the CLI navigation", () => {
    const meta = JSON.parse(read("www/content/docs/cli/meta.json")) as { pages: string[] };
    expect(meta.pages).toContain("logs");
  });

  it("names every field of the JSON entry and notice objects", () => {
    const output = read(sources.output);
    const agents = section("Agents and CI");
    for (const type of ["entryObject", "noticeObject"]) {
      const fields = jsonFieldsOf(output, type);
      expect(fields, type).toContain("type");
      for (const field of fields) expect(agents, `${type}.${field}`).toContain(`\`${field}\``);
    }
  });

  it("gives agents and CI the two commands that end on their own", () => {
    const agents = section("Agents and CI");
    expect(agents).toContain("ocel logs --json --level error --since 30m");
    expect(agents).toContain("ocel logs --json --tail --for 60s");
  });

  it("states the VPS retention the host sets", () => {
    const containers = read(sources.containers);
    const size = Number(containers.match(/logMaxSize\s*=\s*"(\d+)m"/)?.[1]);
    const files = Number(containers.match(/logMaxFiles\s*=\s*"(\d+)"/)?.[1]);
    expect(size).toBeGreaterThan(0);
    expect(files).toBeGreaterThan(0);
    expect(page).toContain(`${size * files} MB`);
    expect(page).toContain(`${files} files of ${size} MB`);
  });

  it("reruns whenever a file it reads changes", () => {
    const turbo = JSON.parse(read("www/turbo.json")) as {
      tasks: { test: { inputs: string[] } };
    };
    for (const path of Object.values(sources)) {
      expect(turbo.tasks.test.inputs).toContain(`$TURBO_ROOT$/${path}`);
    }
  });
});
