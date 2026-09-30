import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { chmod, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, before, describe, it } from "node:test";

const script = new URL("../incus.sh", import.meta.url).pathname;

const incusStub = `#!/usr/bin/env bash
case "$1" in
list) [ "$STUB_GUEST_ADDRESS" = none ] || echo '"10.0.0.5 (enp5s0)"' ;;
exec)
    shift 3
    case "$*" in
    "cloud-init status") echo "status: done" ;;
    *"command -v sshd"*) echo installed ;;
    "ip -o link show up")
        echo "1: lo: <LOOPBACK,UP,LOWER_UP> mtu 65536 state UNKNOWN"
        [ "$STUB_GUEST_LINK" = down ] || echo "2: enp5s0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 state UP" ;;
    esac ;;
config) cat >/dev/null ;;
esac
`;

const exitZeroStub = "#!/usr/bin/env bash\nexit 0\n";

function run(env, name) {
  return new Promise((resolve) => {
    const started = Date.now();
    const child = spawn("bash", [script, "run", name, "--", "true"], {
      env,
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stderr = "";
    child.stderr.on("data", (chunk) => {
      stderr += chunk;
    });
    child.on("close", (code) =>
      resolve({ name, code, stderr, seconds: (Date.now() - started) / 1000 }),
    );
  });
}

describe("incus.sh run", () => {
  let dir;
  let env;

  before(async () => {
    dir = await mkdtemp(join(tmpdir(), "incus-test-"));
    const bin = join(dir, "bin");
    await mkdir(bin);
    await writeFile(join(bin, "incus"), incusStub);
    await chmod(join(bin, "incus"), 0o755);
    for (const stub of ["ssh", "curl", "sudo"]) {
      await writeFile(join(bin, stub), exitZeroStub);
      await chmod(join(bin, stub), 0o755);
    }
    env = {
      ...process.env,
      PATH: `${bin}:${process.env.PATH}`,
      OCEL_INCUS_STATE: join(dir, "state"),
    };
  });

  after(async () => {
    await rm(dir, { recursive: true, force: true });
  });

  it("starts every lane of a fan-out on a host that has no SSH key yet", async () => {
    const names = Array.from({ length: 8 }, (_, i) => `lane-${i}`);
    const results = await Promise.all(names.map((name) => run(env, name)));
    for (const result of results) {
      assert.equal(result.code, 0, `${result.name} exited ${result.code}: ${result.stderr}`);
    }
  });

  it("gives up once cloud-init ends with no interface up but lo, without waiting out its SSH budget", async () => {
    const result = await run(
      { ...env, STUB_GUEST_ADDRESS: "none", STUB_GUEST_LINK: "down", OCEL_INCUS_SSH_WAIT: "60" },
      "no-link",
    );
    assert.notEqual(result.code, 0);
    assert.match(
      result.stderr,
      /no-link: cloud-init ended done after \d+s with no interface up but lo/,
    );
    assert.ok(result.seconds < 30, `took ${result.seconds}s of a 60s budget`);
  });

  it("waits out its SSH budget for a guest whose interface is up but has no address yet", async () => {
    const result = await run(
      { ...env, STUB_GUEST_ADDRESS: "none", OCEL_INCUS_SSH_WAIT: "3" },
      "no-address",
    );
    assert.notEqual(result.code, 0);
    assert.match(
      result.stderr,
      /no-address: no SSH after \d+s, budget 3s \(cloud-init done, sshd installed, address none\)/,
    );
  });
});
