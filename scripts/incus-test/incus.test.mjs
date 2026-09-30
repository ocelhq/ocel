import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { chmod, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, before, describe, it } from "node:test";

const script = new URL("../incus.sh", import.meta.url).pathname;

const incusStub = `#!/usr/bin/env bash
case "$1" in
list) echo '"10.0.0.5 (enp5s0)"' ;;
exec)
    shift 3
    case "$*" in
    "cloud-init status") echo "status: done" ;;
    *"command -v sshd"*) echo installed ;;
    esac ;;
config) cat >/dev/null ;;
esac
`;

const sshStub = "#!/usr/bin/env bash\nexit 0\n";

function run(env, name) {
  return new Promise((resolve) => {
    const child = spawn("bash", [script, "run", name, "--", "true"], {
      env,
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stderr = "";
    child.stderr.on("data", (chunk) => {
      stderr += chunk;
    });
    child.on("close", (code) => resolve({ name, code, stderr }));
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
    await writeFile(join(bin, "ssh"), sshStub);
    await chmod(join(bin, "incus"), 0o755);
    await chmod(join(bin, "ssh"), 0o755);
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
});
