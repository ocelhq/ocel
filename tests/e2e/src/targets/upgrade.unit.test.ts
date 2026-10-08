import { describe, expect, it } from "bun:test";
import { NonzeroExitError, type Ran } from "../ocel";
import {
  BOOTSTRAP_BIN_ENV,
  BOOTSTRAP_PROVIDERS_DIR_ENV,
  bootstrapBuild,
  deployOverOlderBootstrap,
  refusedForOlderBootstrap,
} from "./upgrade";

const BEHIND: Ran = {
  code: 1,
  stdout: "",
  stderr:
    "Error: this account's Ocel bootstrap is behind what this build needs: the release record.\nRun `ocel bootstrap production`, then try again\n",
};

const ABSENT: Ran = {
  code: 1,
  stdout: "",
  stderr:
    "Error: this account has no Ocel bootstrap.\nRun `ocel bootstrap production` to create it, then try again\n",
};

const DEPLOYED: Ran = { code: 0, stdout: "Deployed", stderr: "" };

describe("bootstrapBuild", () => {
  it("bootstraps with the build under test when the lane names no older one", () => {
    expect(bootstrapBuild({})).toBeUndefined();
    expect(bootstrapBuild({ [BOOTSTRAP_BIN_ENV]: " ", [BOOTSTRAP_PROVIDERS_DIR_ENV]: "" })).toBe(
      undefined,
    );
  });

  it("reads the older build and the providers of its own version", () => {
    expect(
      bootstrapBuild({
        [BOOTSTRAP_BIN_ENV]: "/previous/dist/ocel",
        [BOOTSTRAP_PROVIDERS_DIR_ENV]: "/previous/dist/providers",
      }),
    ).toEqual({ bin: "/previous/dist/ocel", providersDir: "/previous/dist/providers" });
  });

  it("refuses an older build named without its providers, which would bootstrap with the newer ones", () => {
    expect(() => bootstrapBuild({ [BOOTSTRAP_BIN_ENV]: "/previous/dist/ocel" })).toThrow(
      BOOTSTRAP_PROVIDERS_DIR_ENV,
    );
    expect(() =>
      bootstrapBuild({ [BOOTSTRAP_PROVIDERS_DIR_ENV]: "/previous/dist/providers" }),
    ).toThrow(BOOTSTRAP_BIN_ENV);
  });
});

describe("refusedForOlderBootstrap", () => {
  it("reads a deploy refused because the bootstrap is behind", () => {
    expect(refusedForOlderBootstrap(BEHIND)).toBe(true);
  });

  it("reads the refusal through the colour a terminal paints it in", () => {
    expect(
      refusedForOlderBootstrap({
        ...BEHIND,
        stderr: `\u001b[31m${BEHIND.stderr.replace("`ocel", "`\u001b[1mocel")}\u001b[0m`,
      }),
    ).toBe(true);
  });

  it("does not take a missing bootstrap for one that is behind", () => {
    expect(refusedForOlderBootstrap(ABSENT)).toBe(false);
  });

  it("does not take any other failure for the refusal", () => {
    expect(refusedForOlderBootstrap({ code: 1, stdout: "", stderr: "Error: build failed" })).toBe(
      false,
    );
  });

  it("does not take a deploy that went ahead for a refusal, whatever it printed", () => {
    expect(refusedForOlderBootstrap({ ...BEHIND, code: 0 })).toBe(false);
  });
});

type Script = {
  deploys: Ran[];
  bootstrap?: Ran;
  projectExists?: boolean;
};

function scripted(script: Script) {
  const ran: string[] = [];
  const deploys = [...script.deploys];
  return {
    ran,
    steps: {
      deploy: async (name: string) => {
        ran.push(name);
        const result = deploys.shift();
        if (!result) {
          throw new Error("deployed more often than the script allows");
        }
        if (result.code !== 0) {
          throw new NonzeroExitError(["deploy", "--yes"], result);
        }
        return result;
      },
      bootstrap: async () => {
        ran.push("bootstrap");
        const result = script.bootstrap ?? DEPLOYED;
        if (result.code !== 0) {
          throw new NonzeroExitError(["bootstrap", "production", "--yes"], result);
        }
        return result;
      },
      projectExists: async () => {
        ran.push("project-exists");
        return script.projectExists ?? false;
      },
    },
  };
}

describe("deployOverOlderBootstrap", () => {
  it("deploys once when the older bootstrap already serves this build", async () => {
    const { ran, steps } = scripted({ deploys: [DEPLOYED] });
    expect(await deployOverOlderBootstrap(steps)).toBe("deployed over the older bootstrap");
    expect(ran).toEqual(["deploy-over-older-bootstrap"]);
  });

  it("bootstraps with this build and deploys again after a clean refusal", async () => {
    const { ran, steps } = scripted({ deploys: [BEHIND, DEPLOYED] });
    expect(await deployOverOlderBootstrap(steps)).toBe(
      "refused over the older bootstrap, deployed after bootstrapping",
    );
    expect(ran).toEqual(["deploy-over-older-bootstrap", "project-exists", "bootstrap", "deploy"]);
  });

  it("fails a refusal that left the project deployed, since it changed something first", async () => {
    const { ran, steps } = scripted({ deploys: [BEHIND, DEPLOYED], projectExists: true });
    await expect(deployOverOlderBootstrap(steps)).rejects.toThrow(/changed something/);
    expect(ran).not.toContain("bootstrap");
  });

  it("fails any other deploy failure rather than bootstrapping over it", async () => {
    const { ran, steps } = scripted({
      deploys: [{ code: 1, stdout: "", stderr: "Error: build failed" }],
    });
    await expect(deployOverOlderBootstrap(steps)).rejects.toThrow(/build failed/);
    expect(ran).toEqual(["deploy-over-older-bootstrap"]);
  });

  it("fails when the deploy after bootstrapping is still refused", async () => {
    const { steps } = scripted({ deploys: [BEHIND, BEHIND] });
    await expect(deployOverOlderBootstrap(steps)).rejects.toThrow(/behind/);
  });
});
