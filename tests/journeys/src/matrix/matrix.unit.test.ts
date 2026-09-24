import { describe, expect, it } from "bun:test";
import { NO_FILTER, plan, type RunFilter } from "../plan";
import { filterFrom } from "../run/filter";
import { hasReleaseCycle, targetNamed } from "../targets";
import { fixtures } from "./fixtures";
import { gaps } from "./gaps";
import { type Concern, LANES, type Lane, targetOfLane } from "./types";

const HELD = {
  OCEL_JOURNEY_REGISTRY_USER: "octocat",
  OCEL_JOURNEY_REGISTRY_TOKEN: "ghs_t0ken",
};

function planOn(lane: Lane, env: NodeJS.ProcessEnv = {}, filter: RunFilter = NO_FILTER) {
  const releaseCycle = hasReleaseCycle(targetNamed(targetOfLane(lane)));
  return plan({ fixtures, gaps, lane, releaseCycle, filter, env });
}

function concernIn(concern: Concern, planned: ReturnType<typeof planOn>): string[] {
  return [...planned.cells.map((cell) => cell.name), ...Object.keys(planned.skipped)].filter(
    (name) => name.startsWith(`${concern}/`),
  );
}

describe("the journey matrix", () => {
  it("plans on every lane without a dead gap, with the registry held and without", () => {
    for (const lane of LANES) {
      expect(() => planOn(lane)).not.toThrow();
      expect(() => planOn(lane, HELD)).not.toThrow();
    }
  });

  it("files the fixtures an SST or Pulumi stack stands up under iac, and only those", () => {
    const named = (concern: Concern) => fixtures.filter((one) => one.concern === concern);
    const stacked = fixtures.filter((one) => one.stack !== undefined);
    expect(named("sdk").filter((one) => one.stack !== undefined)).toEqual([]);
    expect(named("iac")).toEqual(stacked);
    expect(stacked).not.toEqual([]);
  });
});

describe("the iac concern", () => {
  const IAC_LANES = ["aws", "aws.floci"] as const;

  it("plans no iac cell for a run that leaves the concern unnamed", () => {
    for (const lane of IAC_LANES) {
      expect(concernIn("iac", planOn(lane, {}, { ...NO_FILTER, runSkipped: true }))).toEqual([]);
      expect(concernIn("iac", planOn(lane, {}, filterFrom({ OCEL_JOURNEY_SKIPS: "run" })))).toEqual(
        [],
      );
    }
  });

  it("plans no iac cell for a pull request whose diff touches the iac fixtures", () => {
    const touched = {
      OCEL_JOURNEY_SEED: "42",
      OCEL_JOURNEY_TOUCHED: "iac/with-sst,iac/with-pulumi",
    };
    for (const lane of IAC_LANES) {
      expect(concernIn("iac", planOn(lane, {}, filterFrom(touched)))).toEqual([]);
      expect(
        concernIn("iac", planOn(lane, {}, filterFrom({ ...touched, OCEL_JOURNEY_SKIPS: "run" }))),
      ).toEqual([]);
    }
  });

  it("plans the iac cells a run names the concern for", () => {
    for (const lane of IAC_LANES) {
      const planned = planOn(
        lane,
        {},
        filterFrom({ OCEL_JOURNEY_CONCERN: "iac", OCEL_JOURNEY_SKIPS: "run" }),
      );
      expect(planned.cells.map((cell) => cell.fixture)).toContain("iac/with-sst");
      expect(planned.cells.map((cell) => cell.fixture)).toContain("iac/with-pulumi");
    }
  });

  it("refuses an iac fixture named under a run that does not name iac", () => {
    for (const concern of [undefined, "sdk", "deploy lifecycle sdk"]) {
      const filter = filterFrom({
        OCEL_JOURNEY_CONCERN: concern,
        OCEL_JOURNEY_FIXTURES: "iac/with-sst",
      });
      expect(() => planOn("aws", {}, filter)).toThrow(/no fixture named iac\/with-sst/);
    }
  });
});

describe("a pull request's run", () => {
  it("draws sdk cells when it names no concern", () => {
    const drawn = filterFrom({ OCEL_JOURNEY_SEED: "42", OCEL_JOURNEY_TOUCHED: "" });
    for (const lane of ["aws.floci", "dev", "vps.incus"] as const) {
      expect(concernIn("sdk", planOn(lane, {}, drawn))).not.toEqual([]);
    }
  });
});

describe("the registry variant", () => {
  it("deploys through the registry on either box when the run holds its credentials", () => {
    for (const lane of ["vps", "vps.incus"] as const) {
      expect(planOn(lane, HELD).cells.map((cell) => cell.name)).toContain("deploy/node-registry");
    }
  });

  it("is skipped on either box when the run lacks the user or the token it pushes with", () => {
    const { OCEL_JOURNEY_REGISTRY_USER: _user, ...tokenOnly } = HELD;
    const { OCEL_JOURNEY_REGISTRY_TOKEN: _token, ...userOnly } = HELD;
    for (const lane of ["vps", "vps.incus"] as const) {
      for (const env of [{}, tokenOnly, userOnly]) {
        const planned = planOn(lane, env);
        expect(planned.cells.map((cell) => cell.name)).not.toContain("deploy/node-registry");
        expect(planned.skipped["deploy/node-registry"]?.map((gap) => gap.id)).toEqual([
          "no-registry-credentials",
        ]);
      }
    }
  });
});
