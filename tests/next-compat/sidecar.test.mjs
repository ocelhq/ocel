import { mkdirSync, mkdtempSync, readlinkSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

import { linkSidecar } from "./sidecar.mjs";

describe("linkSidecar", () => {
  it("links a sidecar packed from the ocel tarball alone", () => {
    const root = mkdtempSync(join(tmpdir(), "sidecar-"));
    const sidecar = join(root, "sidecar");
    const app = join(root, "app");
    mkdirSync(join(sidecar, "node_modules", "ocel"), { recursive: true });
    mkdirSync(app);

    linkSidecar(app, sidecar);

    expect(readlinkSync(join(app, "node_modules", "ocel"))).toBe(
      join(sidecar, "node_modules", "ocel"),
    );
  });

  it("refuses a sidecar with no ocel package", () => {
    const root = mkdtempSync(join(tmpdir(), "sidecar-"));
    mkdirSync(join(root, "sidecar"));
    mkdirSync(join(root, "app"));

    expect(() => linkSidecar(join(root, "app"), join(root, "sidecar"))).toThrow(
      /sidecar has no ocel package/,
    );
  });
});
