import { execFileSync, spawn } from "node:child_process";
import { createHash } from "node:crypto";
import {
  createReadStream,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  statSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { afterAll, beforeAll, describe, expect, it } from "vitest";

const repo = join(dirname(fileURLToPath(import.meta.url)), "..", "..", "..");
const installer = join(repo, "www", "public", "install.sh");

const version = "1.2.3";
const marker = "ocel 1.2.3 from the fake release";
const targets = [
  ["linux", "amd64"],
  ["linux", "arm64"],
  ["darwin", "amd64"],
  ["darwin", "arm64"],
];

let root = "";
let origin = "";
let server;

function archive(directory, name) {
  const staging = mkdtempSync(join(tmpdir(), "ocel-archive-"));
  writeFileSync(join(staging, "ocel"), `#!/bin/sh\necho "${marker}"\n`, { mode: 0o755 });
  execFileSync("tar", ["-czf", join(directory, name), "-C", staging, "ocel"]);
  rmSync(staging, { recursive: true, force: true });
  return createHash("sha256")
    .update(readFileSync(join(directory, name)))
    .digest("hex");
}

function release(directory, sum) {
  mkdirSync(directory, { recursive: true });
  const lines = targets.map(([os, arch]) => {
    const name = `ocel_${version}_${os}_${arch}.tar.gz`;
    return `${sum(archive(directory, name))}  ${name}`;
  });
  writeFileSync(join(directory, "checksums.txt"), `${lines.join("\n")}\n`);
}

beforeAll(async () => {
  root = mkdtempSync(join(tmpdir(), "ocel-release-"));
  release(join(root, "download", `v${version}`), (sum) => sum);
  release(join(root, "tampered", `v${version}`), () => "0".repeat(64));
  writeFileSync(join(root, "latest.json"), JSON.stringify({ tag_name: `v${version}` }));

  server = createServer((request, response) => {
    const path = join(root, decodeURIComponent(new URL(request.url, "http://x").pathname));
    if (!path.startsWith(root) || !existsSync(path)) {
      response.writeHead(404).end("not found");
      return;
    }
    response.writeHead(200);
    createReadStream(path).pipe(response);
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  origin = `http://127.0.0.1:${server.address().port}`;
});

afterAll(async () => {
  rmSync(root, { recursive: true, force: true });
  await new Promise((resolve) => server.close(resolve));
});

function install({ env = {}, path, seed } = {}) {
  const home = mkdtempSync(join(tmpdir(), "ocel-home-"));
  seed?.(home);
  const child = spawn("sh", [installer], {
    env: {
      PATH: path ? `${path}:${process.env.PATH}` : process.env.PATH,
      HOME: home,
      OCEL_INSTALL_DOWNLOADS: `${origin}/download`,
      OCEL_INSTALL_LATEST: `${origin}/latest.json`,
      ...env,
    },
  });
  let stdout = "";
  let stderr = "";
  child.stdout.on("data", (chunk) => {
    stdout += chunk;
  });
  child.stderr.on("data", (chunk) => {
    stderr += chunk;
  });
  return new Promise((resolve) => {
    child.on("close", (status) => {
      resolve({ status, stdout, stderr, home, installed: join(home, ".local", "bin", "ocel") });
    });
  });
}

function stubbedUname(machine) {
  const bin = mkdtempSync(join(tmpdir(), "ocel-stub-"));
  const kernel = execFileSync("uname", ["-s"], { encoding: "utf8" }).trim();
  writeFileSync(
    join(bin, "uname"),
    `#!/bin/sh\ncase "$1" in\n-m) echo ${machine} ;;\n*) echo ${kernel} ;;\nesac\n`,
    { mode: 0o755 },
  );
  return bin;
}

describe.runIf(process.platform !== "win32")("install.sh", () => {
  it("installs the pinned release into ~/.local/bin", async () => {
    const run = await install({ env: { OCEL_VERSION: version } });
    expect(run.stderr).toBe("");
    expect(run.status).toBe(0);
    expect(existsSync(run.installed)).toBe(true);
    expect(statSync(run.installed).mode & 0o111).toBeGreaterThan(0);
    expect(execFileSync(run.installed, { encoding: "utf8" })).toBe(`${marker}\n`);
    rmSync(run.home, { recursive: true, force: true });
  });

  it("tells the reader how to put ~/.local/bin on PATH", async () => {
    const run = await install({ env: { OCEL_VERSION: version } });
    expect(run.stdout).toContain(join(run.home, ".local", "bin"));
    expect(run.stdout).toMatch(/PATH/);
    rmSync(run.home, { recursive: true, force: true });
  });

  it("takes the version from the latest release when none is pinned", async () => {
    const run = await install();
    expect(run.stderr).toBe("");
    expect(run.status).toBe(0);
    expect(run.stdout).toContain(version);
    expect(execFileSync(run.installed, { encoding: "utf8" })).toBe(`${marker}\n`);
    rmSync(run.home, { recursive: true, force: true });
  });

  it("names OCEL_VERSION when the latest release cannot be read", async () => {
    const run = await install({ env: { OCEL_INSTALL_LATEST: `${origin}/absent.json` } });
    expect(run.status).not.toBe(0);
    expect(run.stderr).toContain("OCEL_VERSION");
    expect(existsSync(run.installed)).toBe(false);
    rmSync(run.home, { recursive: true, force: true });
  });

  it("replaces a symlink already sitting at the destination", async () => {
    const run = await install({
      env: { OCEL_VERSION: version },
      seed: (home) => {
        const bin = join(home, ".local", "bin");
        mkdirSync(bin, { recursive: true });
        const decoy = join(home, "decoy");
        writeFileSync(decoy, "#!/bin/sh\necho decoy\n", { mode: 0o755 });
        symlinkSync(decoy, join(bin, "ocel"));
      },
    });
    expect(run.status).toBe(0);
    expect(lstatSync(run.installed).isSymbolicLink()).toBe(false);
    expect(readFileSync(join(run.home, "decoy"), "utf8")).toBe("#!/bin/sh\necho decoy\n");
    expect(execFileSync(run.installed, { encoding: "utf8" })).toBe(`${marker}\n`);
    rmSync(run.home, { recursive: true, force: true });
  });

  it("installs nothing when the archive does not match checksums.txt", async () => {
    const run = await install({
      env: { OCEL_VERSION: version, OCEL_INSTALL_DOWNLOADS: `${origin}/tampered` },
    });
    expect(run.status).not.toBe(0);
    expect(run.stderr).toMatch(/checksum/i);
    expect(existsSync(run.installed)).toBe(false);
    rmSync(run.home, { recursive: true, force: true });
  });

  it("installs nothing when the release has no archive for this platform", async () => {
    const run = await install({ env: { OCEL_VERSION: "0.0.0" } });
    expect(run.status).not.toBe(0);
    expect(existsSync(run.installed)).toBe(false);
    rmSync(run.home, { recursive: true, force: true });
  });

  it("names the machine it has no binary for", async () => {
    const run = await install({ env: { OCEL_VERSION: version }, path: stubbedUname("riscv64") });
    expect(run.status).not.toBe(0);
    expect(run.stderr).toContain("riscv64");
    expect(existsSync(run.installed)).toBe(false);
    rmSync(run.home, { recursive: true, force: true });
  });
});
