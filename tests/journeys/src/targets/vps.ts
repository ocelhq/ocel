import { execFile } from "node:child_process";
import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { promisify } from "node:util";
import { HARNESS_ONLY_ENV } from "@ocel-tests/shared/env";
import { migrates, setsEnv } from "../checks";
import {
  INITIAL_GREETING,
  REDACTED,
  redact,
  SECRET_TOKEN,
  UNCAPPED_BODY_BYTES,
} from "../checks/context";
import { journeyConfigIn, journeyZone } from "../config";
import { appHostname, HARNESS_PREFIX, isStranded } from "../identity";
import type { Lane, Phase } from "../matrix/types";
import { exitedBadly, ocel, runOcel, spawnOcel, workTree } from "../ocel";
import { outputRoot } from "../paths";
import type { PrepareFailures } from "../prepare";
import type { CellUnderTest } from "../run/cellRun";
import { migrateCommand } from "../workspace";
import { type Gateway, openGateway } from "./gateway";
import type { Deployment, ReleaseCycle, Sweeper, Target } from "./types";

const DEFAULT_ZONE = "localhost";
const DEPLOY_LOGIN = "ocel-deploy";
const INCUS_MARKER = "/dev/virtio-ports/org.linuxcontainers.incus";
const PROJECT_RECORDS = "/var/lib/ocel/production/records/projects/production";
const PROXY_ROOT = "/var/lib/ocel/proxy/data/caddy/pki/authorities/local/root.crt";
const NO_RECORDS_TIER = "no-records-tier";
const ROOT_WAIT_MS = 180_000;
const ROOT_FIRST_WAIT_MS = 500;
const ROOT_LONGEST_WAIT_MS = 5_000;

const BRING_A_BOX_UP = [
  "scripts/incus.sh create <name>",
  'eval "$(scripts/incus.sh info <name>)"',
  "export OCEL_VPS_HOST=$OCEL_INCUS_ADDR OCEL_VPS_USER=$OCEL_INCUS_USER OCEL_VPS_IDENTITY_FILE=$OCEL_INCUS_KEY",
].join("\n  ");

export type Box = { host: string; user: string; identityFile: string };

type BoxSession = { dir: string; gateway: Gateway; env: NodeJS.ProcessEnv };

const ran = promisify(execFile);

export function boxLane(said: string): Lane {
  const verdict = said.trim();
  if (verdict === "incus") {
    return "vps.incus";
  }
  if (verdict === "real") {
    return "vps";
  }
  throw new Error(
    `the box answered ${JSON.stringify(verdict)} when asked whether it runs under incus`,
  );
}

export function issuedByTheBox(zone: string): boolean {
  return zone === DEFAULT_ZONE || zone.endsWith(`.${DEFAULT_ZONE}`);
}

export function recordFile(slug: string): string {
  const encoded = Array.from(Buffer.from(slug, "utf8"), (byte, at) => {
    const char = String.fromCharCode(byte);
    const plain = /^[A-Za-z0-9\-_.]$/.test(char) && !(at === 0 && char === ".");
    return plain ? char : `%${byte.toString(16).toUpperCase().padStart(2, "0")}`;
  }).join("");
  return `${encoded}.rec`;
}

export function slugsOf(listing: string): string[] {
  if (listing.trim() === NO_RECORDS_TIER) {
    throw new Error(
      `${PROJECT_RECORDS} does not exist on the box, so nothing here can tell a box that holds ` +
        "no harness project from one this listing failed to read",
    );
  }
  return listing
    .split("\n")
    .map((line) => line.trim().split("/").pop() ?? "")
    .filter((name) => name.endsWith(".rec"))
    .map((name) => decodeURIComponent(name.slice(0, -".rec".length)));
}

export function sshRefusal(target: Box, login: string, command: string, error: unknown): Error {
  const said = error as { code?: number | string; stderr?: unknown };
  const shown = (text: string) => redact(text).split(target.identityFile).join(REDACTED);
  const status = said?.code === undefined ? "without a status" : `${said.code}`;
  const stderr = typeof said?.stderr === "string" ? shown(said.stderr).trim() : "";
  return new Error(
    `ssh ${login}@${target.host} exited ${status} running ${shown(command)}` +
      `${stderr ? `\n${stderr}` : ""}`,
  );
}

function readBox(): Box {
  const host = process.env.OCEL_VPS_HOST;
  const user = process.env.OCEL_VPS_USER;
  const identityFile = process.env.OCEL_VPS_IDENTITY_FILE;
  if (!host || !user || !identityFile) {
    throw new Error(
      "OCEL_VPS_HOST, OCEL_VPS_USER and OCEL_VPS_IDENTITY_FILE name the box this target deploys to, " +
        `and the journey harness never brings one up. Run:\n  ${BRING_A_BOX_UP}`,
    );
  }
  return { host, user, identityFile };
}

export async function ssh(target: Box, login: string, command: string): Promise<string> {
  try {
    const { stdout } = await ran("ssh", [
      "-i",
      target.identityFile,
      "-o",
      "IdentitiesOnly=yes",
      "-o",
      "BatchMode=yes",
      "-o",
      "StrictHostKeyChecking=accept-new",
      "-o",
      "ConnectTimeout=10",
      `${login}@${target.host}`,
      command,
    ]);
    return stdout;
  } catch (error) {
    throw sshRefusal(target, login, command, error);
  }
}

export class VpsTarget implements Target, ReleaseCycle {
  readonly name = "vps";
  readonly workers = 2;
  readonly maxRequestBodyBytes = UNCAPPED_BODY_BYTES;
  readonly stepTimeoutMs = 600_000;

  private readonly sessions = new Map<string, BoxSession>();
  private resolvedBox: Box | undefined;
  private resolvedZone: string | undefined;

  readonly sweeper: Sweeper = {
    list: () => this.recordedSlugs(),
    exists: (slug) => this.stillRecorded(slug),
    sweepStale: (runId) => this.sweepStale(runId),
    sweepRun: async () => {},
  };

  async detectLane(): Promise<Lane> {
    const target = this.box();
    let said: string;
    try {
      said = await ssh(target, target.user, `test -e ${INCUS_MARKER} && echo incus || echo real`);
    } catch (error) {
      throw new Error(
        `${target.user}@${target.host} does not answer over ssh, and the journey harness never ` +
          `brings a box up. Run:\n  ${BRING_A_BOX_UP}\n\n${(error as Error).message}`,
      );
    }
    return boxLane(said);
  }

  async prepareLane(): Promise<PrepareFailures> {
    await this.detectLane();
    const target = this.box();
    const dir = await this.boxConfig(
      path.join(outputRoot, "vps", "box"),
      `${HARNESS_PREFIX}journey-bootstrap`,
      target.user,
    );
    const args = ["bootstrap", "production", "--yes"];
    const result = await spawnOcel(dir, args, this.boxEnv(target.user));
    const log = redact(`${result.stdout}${result.stderr}`);
    await writeFile(path.join(dir, "bootstrap.log"), log, "utf8");
    if (result.code !== 0) {
      throw exitedBadly(args, result);
    }
    return {};
  }

  async prepareProcess(): Promise<void> {
    await this.detectLane();
  }

  async deploy(cell: CellUnderTest): Promise<Deployment> {
    const session = await this.sessionFor(cell);
    const drive = this.driving(cell, session, "deploy");

    if (setsEnv(cell.fixture.checks)) {
      await drive("env-greeting", ["env", "set", `GREETING=${INITIAL_GREETING}`]);
      await drive("env-secret", ["env", "set", `SECRET_TOKEN=${SECRET_TOKEN}`]);
    }
    await drive("deploy", ["deploy", "--yes"]);
    await this.bindDomains(cell, session);
    if (migrates(cell.fixture.checks)) {
      await drive("migrate", ["run", "--", ...migrateCommand()]);
    }
    return this.deployment(cell, session);
  }

  async redeploy(cell: CellUnderTest, greeting: string): Promise<Deployment> {
    const session = await this.sessionFor(cell);
    const drive = this.driving(cell, session, "redeploy");

    if (setsEnv(cell.fixture.checks)) {
      await drive("env-greeting", ["env", "set", `GREETING=${greeting}`]);
    }
    await drive("deploy", ["deploy", "--yes"]);
    return this.deployment(cell, session);
  }

  async rollback(cell: CellUnderTest): Promise<Deployment> {
    const session = await this.sessionFor(cell);

    await this.driving(cell, session, "rollback")("rollback", ["rollback", "--yes"]);
    return this.deployment(cell, session);
  }

  async destroy(cell: CellUnderTest): Promise<void> {
    const session = this.sessions.get(cell.slug);
    if (!session) {
      return;
    }
    this.sessions.delete(cell.slug);
    const args = ["--config", journeyConfigIn(session.dir), "destroy", "production", "--yes"];
    try {
      await runOcel(cell, session.dir, "destroy", "destroy", args, session.env);
    } catch (refused) {
      if (await this.stillRecorded(cell.slug)) {
        throw refused;
      }
    } finally {
      await session.gateway.close();
    }
  }

  private box(): Box {
    this.resolvedBox ??= readBox();
    return this.resolvedBox;
  }

  private zone(): string {
    this.resolvedZone ??= journeyZone(process.env);
    return this.resolvedZone;
  }

  private boxEnv(login: string): NodeJS.ProcessEnv {
    const target = this.box();
    const env: NodeJS.ProcessEnv = {
      ...process.env,
      OCEL_VPS_HOST: target.host,
      OCEL_VPS_USER: login,
      OCEL_VPS_IDENTITY_FILE: target.identityFile,
    };
    for (const name of HARNESS_ONLY_ENV) {
      delete env[name];
    }
    return env;
  }

  private async boxConfig(dir: string, slug: string, login: string): Promise<string> {
    const target = this.box();
    await mkdir(dir, { recursive: true });
    await writeFile(
      path.join(dir, "ocel.json"),
      `${JSON.stringify(
        {
          slug,
          provider: {
            name: "vps",
            options: {
              ssh: { host: target.host, user: login, identityFile: target.identityFile },
            },
          },
          apps: [],
        },
        null,
        2,
      )}\n`,
      "utf8",
    );
    return dir;
  }

  private async trusted(cell: CellUnderTest): Promise<string | undefined> {
    if (!issuedByTheBox(this.zone())) {
      return undefined;
    }
    const target = this.box();
    const deadline = Date.now() + ROOT_WAIT_MS;
    let wait = ROOT_FIRST_WAIT_MS;
    while (true) {
      const root = await ssh(target, target.user, `sudo cat ${PROXY_ROOT} 2>/dev/null || true`);
      if (root.includes("BEGIN CERTIFICATE")) {
        await cell.evidence.write("deploy", "proxy-root.pem", root);
        return path.join(cell.evidence.dir, "deploy", "proxy-root.pem");
      }
      if (Date.now() >= deadline) {
        throw new Error(
          `${PROXY_ROOT} holds no issuing root ${ROOT_WAIT_MS / 1000}s after the deploy, and every ` +
            `hostname on ${this.zone()} is settled against a certificate this box issues itself`,
        );
      }
      await delay(wait);
      wait = Math.min(wait * 2, ROOT_LONGEST_WAIT_MS);
    }
  }

  private async bindDomains(cell: CellUnderTest, session: BoxSession): Promise<void> {
    const root = await this.trusted(cell);
    await runOcel(
      cell,
      session.dir,
      "deploy",
      "domain-add",
      ["--config", journeyConfigIn(session.dir), "domain", "add"],
      {
        ...session.env,
        HTTPS_PROXY: session.gateway.tunnelUrl,
        ...(root ? { SSL_CERT_FILE: root } : {}),
      },
    );
  }

  private hostnamesOf(cell: CellUnderTest): Map<string, string> {
    return new Map(
      cell.fixture.apps.map((app) => {
        const hostname = appHostname(app, cell.slug, this.zone());
        if (!hostname) {
          throw new Error(`${app} has no hostname on ${this.zone()}`);
        }
        return [app, hostname];
      }),
    );
  }

  private async deployment(cell: CellUnderTest, session: BoxSession): Promise<Deployment> {
    const urls = new Map<string, string>();
    for (const [app, hostname] of this.hostnamesOf(cell)) {
      urls.set(app, await session.gateway.serving(hostname));
    }
    await cell.evidence.write(
      "deploy",
      "deployment.json",
      `${JSON.stringify({ slug: cell.slug, zone: this.zone(), apps: Object.fromEntries(urls) }, null, 2)}\n`,
    );
    return {
      baseUrl: (app) => {
        const url = urls.get(app);
        if (!url) {
          throw new Error(`${cell.name} has no app named ${app} on vps`);
        }
        return url;
      },
      fetch: (input, init) => this.reaching(session, input, init),
    };
  }

  private async reaching(
    session: BoxSession,
    input: string | URL | Request,
    init?: RequestInit,
  ): Promise<Response> {
    const asked = typeof input === "string" || input instanceof URL ? input.toString() : input.url;
    const url = new URL(asked);
    if (url.hostname === `${this.zone()}` || !url.hostname.endsWith(`.${this.zone()}`)) {
      return fetch(input, init);
    }
    const front = new URL(await session.gateway.serving(url.hostname));
    url.protocol = front.protocol;
    url.host = front.host;
    return fetch(url, init);
  }

  private async sessionFor(cell: CellUnderTest): Promise<BoxSession> {
    const already = this.sessions.get(cell.slug);
    if (already) {
      return already;
    }
    const session: BoxSession = {
      dir: await workTree(cell, "vps"),
      gateway: await openGateway(this.box().host),
      env: this.boxEnv(DEPLOY_LOGIN),
    };
    this.sessions.set(cell.slug, session);
    return session;
  }

  private driving(cell: CellUnderTest, session: BoxSession, phase: Phase) {
    return (name: string, args: string[]) =>
      runOcel(
        cell,
        session.dir,
        phase,
        name,
        ["--config", journeyConfigIn(session.dir), ...args],
        session.env,
      );
  }

  private async stillRecorded(slug: string): Promise<boolean> {
    const said = await ssh(
      this.box(),
      DEPLOY_LOGIN,
      `test -e '${PROJECT_RECORDS}/${recordFile(slug)}' && echo held || echo gone`,
    );
    return said.trim() === "held";
  }

  private async recordedSlugs(): Promise<string[]> {
    const listing = await ssh(
      this.box(),
      DEPLOY_LOGIN,
      `test -d '${PROJECT_RECORDS}' && { ls -1d '${PROJECT_RECORDS}'/${HARNESS_PREFIX}*.rec 2>/dev/null || true; } || echo ${NO_RECORDS_TIER}`,
    );
    return slugsOf(listing);
  }

  private async sweepStale(runId: string): Promise<void> {
    const lane = await this.detectLane();
    if (lane !== "vps.incus") {
      throw new Error(
        "sweep destroys every project a harness run left on the box, and this box is not the disposable incus one: " +
          "reclaim a real box by naming what to destroy yourself",
      );
    }
    const stranded = (await this.recordedSlugs()).filter((slug) => isStranded(slug, runId));
    for (const slug of stranded) {
      const dir = await this.boxConfig(
        path.join(outputRoot, "vps", "sweep", slug),
        slug,
        DEPLOY_LOGIN,
      );
      await ocel(dir, ["destroy", "production", "--yes"], this.boxEnv(DEPLOY_LOGIN));
    }
  }
}
