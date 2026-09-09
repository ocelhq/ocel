import { describe, expect, it } from "vitest";
import { binaryPath, platformPackage } from "../bin/resolve.js";

const tuples = [
  ["darwin", "arm64", "@ocel/cli-darwin-arm64", "ocel"],
  ["darwin", "x64", "@ocel/cli-darwin-x64", "ocel"],
  ["linux", "x64", "@ocel/cli-linux-x64", "ocel"],
  ["linux", "arm64", "@ocel/cli-linux-arm64", "ocel"],
  ["win32", "x64", "@ocel/cli-win32-x64", "ocel.exe"],
];

describe("platformPackage", () => {
  for (const [platform, arch, name] of tuples) {
    it(`names ${name} for ${platform}-${arch}`, () => {
      expect(platformPackage(platform, arch)).toBe(name);
    });
  }

  it("names the unsupported platform in the error", () => {
    expect(() => platformPackage("freebsd", "riscv64")).toThrowError(/freebsd-riscv64/);
  });

  it("names an unsupported architecture of a supported os", () => {
    expect(() => platformPackage("win32", "arm64")).toThrowError(/win32-arm64/);
  });
});

describe("binaryPath", () => {
  for (const [platform, arch, name, binary] of tuples) {
    it(`resolves ${name}/bin/${binary} for ${platform}-${arch}`, () => {
      const asked = [];
      const path = binaryPath({
        platform,
        arch,
        resolve: (specifier) => {
          asked.push(specifier);
          return `/store/${specifier}`;
        },
      });
      expect(asked).toEqual([`${name}/bin/${binary}`]);
      expect(path).toBe(`/store/${name}/bin/${binary}`);
    });
  }

  it("names the missing platform package when resolution fails", () => {
    expect(() =>
      binaryPath({
        platform: "linux",
        arch: "x64",
        resolve: () => {
          throw new Error("MODULE_NOT_FOUND");
        },
      }),
    ).toThrowError(/@ocel\/cli-linux-x64/);
  });

  it("refuses an unsupported platform before resolving anything", () => {
    let asked = 0;
    expect(() =>
      binaryPath({
        platform: "aix",
        arch: "ppc64",
        resolve: () => {
          asked += 1;
          return "";
        },
      }),
    ).toThrowError(/aix-ppc64/);
    expect(asked).toBe(0);
  });
});
