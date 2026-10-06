import { describe, expect, it } from "bun:test";
import { previewReleasesIn } from "./previewResult";

const summary = (apps: unknown[]) => JSON.stringify({ summary: { success: true, apps } });

const record = JSON.stringify({ apps: [{ name: "web", deploymentId: "abc" }] });

describe("previewReleasesIn", () => {
  it("reads each app's urls, deployment url and deployment id from the run's summary and record", () => {
    const stream = [
      JSON.stringify({ operation: { name: "preview" } }),
      summary([{ app: "web", urls: ["https://a"], deploymentUrl: "https://t---a" }]),
    ].join("\n");

    expect(previewReleasesIn(stream, record, "ocel preview up journey")).toEqual([
      { app: "web", urls: ["https://a"], deploymentUrl: "https://t---a", deploymentId: "abc" },
    ]);
  });

  it("ignores lines that are not JSON", () => {
    const stream = [
      "building…",
      summary([{ app: "web", deploymentUrl: "https://t---a" }]),
      "done",
    ].join("\n");

    expect(previewReleasesIn(stream, record, "ocel preview up journey")).toEqual([
      { app: "web", urls: [], deploymentUrl: "https://t---a", deploymentId: "abc" },
    ]);
  });

  it("refuses a stream with no summary naming its apps, naming the command", () => {
    expect(() =>
      previewReleasesIn('{"operation":{}}\n', record, "ocel preview up journey"),
    ).toThrow(/`ocel preview up journey` streamed no summary naming its apps/);
    expect(() =>
      previewReleasesIn(JSON.stringify({ summary: { success: true } }), record, "ocel x"),
    ).toThrow(/streamed no summary naming its apps/);
  });

  it("refuses an app the run published no deployment url for", () => {
    expect(() =>
      previewReleasesIn(
        summary([{ app: "web", urls: ["https://a"] }]),
        record,
        "ocel preview up j",
      ),
    ).toThrow(/`ocel preview up j` published no deployment url for web/);
  });

  it("refuses an app the record holds no deployment id for", () => {
    expect(() =>
      previewReleasesIn(
        summary([{ app: "web", deploymentUrl: "https://t" }]),
        JSON.stringify({ apps: [] }),
        "ocel preview up j",
      ),
    ).toThrow(/\.ocel\/deploy-result\.json.*web/);
  });
});
