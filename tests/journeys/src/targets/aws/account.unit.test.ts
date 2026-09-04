import assert from "node:assert/strict";
import path from "node:path";
import { describe, it } from "bun:test";
import { accountFiles, DEFAULT_REGION, pinnedEnv } from "./account";

const DIR = "/tmp/journeys/aws-account";

describe("pinnedEnv", () => {
  it("points a populated local account at the harness's own empty files and drops the profiles", () => {
    const pinned = pinnedEnv(
      {
        AWS_PROFILE: "work",
        AWS_DEFAULT_PROFILE: "work",
        AWS_CONFIG_FILE: "/home/someone/.aws/config",
        AWS_SHARED_CREDENTIALS_FILE: "/home/someone/.aws/credentials",
      },
      DIR,
    );
    assert.equal(pinned.AWS_CONFIG_FILE, path.join(DIR, "config"));
    assert.equal(pinned.AWS_SHARED_CREDENTIALS_FILE, path.join(DIR, "credentials"));
    assert.equal(pinned.AWS_PROFILE, undefined);
    assert.equal(pinned.AWS_DEFAULT_PROFILE, undefined);
    assert.ok(!("AWS_PROFILE" in pinned));
    assert.ok(!("AWS_DEFAULT_PROFILE" in pinned));
    assert.deepEqual(accountFiles(DIR), {
      config: pinned.AWS_CONFIG_FILE,
      credentials: pinned.AWS_SHARED_CREDENTIALS_FILE,
    });
  });

  it("names a region when nothing else does and keeps the one that does", () => {
    assert.equal(pinnedEnv({}, DIR).AWS_REGION, DEFAULT_REGION);
    assert.equal(pinnedEnv({}, DIR).AWS_DEFAULT_REGION, DEFAULT_REGION);
    const named = pinnedEnv({ AWS_REGION: "eu-west-1" }, DIR);
    assert.equal(named.AWS_REGION, "eu-west-1");
    assert.equal(named.AWS_DEFAULT_REGION, "eu-west-1");
  });

  it("leaves the env it was handed alone", () => {
    const env = { AWS_PROFILE: "work" };
    pinnedEnv(env, DIR);
    assert.equal(env.AWS_PROFILE, "work");
  });
});
