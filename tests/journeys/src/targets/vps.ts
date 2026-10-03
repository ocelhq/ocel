import { execFile, spawn } from "node:child_process";
import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { promisify } from "node:util";
import { HARNESS_ONLY_ENV } from "@ocel-tests/shared/env";
import { migrates, setsEnv, setsSecret } from "../checks";
import {
  INITIAL_GREETING,
  JOURNEY_NONCE_ENV,
  REDACTED,
  redact,
  SECRET_TOKEN,
  setsJourneyNonce,
  UNCAPPED_BODY_BYTES,
} from "../checks/context";
import { BOX_ZONE, journeyConfigIn, journeyZone, vpsZoneOf } from "../config";
import { appHostname, HARNESS_PREFIX, isStranded } from "../identity";
import type { Lane, Phase } from "../matrix/types";
import { NonzeroExitError, ocel, recordOutput, runOcel, spawnOcel, workTree } from "../ocel";
import { fixtureMember, outputRoot } from "../paths";
import type { PrepareFailures } from "../prepare";
import type { CellUnderTest } from "../run/cellRun";
import { migrateCommand } from "../workspace";
import { cloudflareUrls } from "./cloudflare";
import { bootstrappedMissed, unbootstrappedMissed } from "./doctor";
import { adoptionMissed, engineSeries } from "./engine";
import {
  coveredNames,
  type Front,
  frontNamed,
  frontStep,
  ownerOf,
  refusalMissed,
  schemeOf,
  stepCommand,
} from "./front";
import { type Gateway, openGateway, type Scheme } from "./gateway";
import { plannedWrites } from "./planStream";
import type { Deployment, Exposure, ReleaseCycle, Restart, Sweeper, Target } from "./types";

const DEPLOY_LOGIN = "ocel-deploy";
const INCUS_MARKER = "/dev/virtio-ports/org.linuxcontainers.incus";
const KEYVALUES_TIER = "/var/lib/ocel/production/keyvalues";
const PROJECT_ENTRIES = `${KEYVALUES_TIER}/projects`;
const NO_KEYVALUES_TIER = "no-keyvalues-tier";
const ENTRY_SUFFIX = ".json";
const CONTAINER_APP_ROOT = "/app";
const CONTAINER_LIVE_DIR = "/ocel/live";
const BOOTSTRAP_DIR = path.join(outputRoot, "vps", "box");
const BOOTSTRAP_SLUG = `${HARNESS_PREFIX}journey-bootstrap`;
const BOOTSTRAP_REMOVED = "Removed the production bootstrap";
const KNOWN_HOSTS_REMEDY = "ssh-keygen -R";
const OCEL_CONTAINERS = ["ocel-proxy", "ocel-switchboard"];
const OCEL_LABELS = ["ocel.app", "ocel.project"];
const OCEL_PATHS = [
  "/etc/ocel",
  "/var/lib/ocel",
  "/usr/local/lib/ocel",
  "/etc/sudoers.d/ocel-seal-production",
];
const STAMP = "/etc/ocel/production/stamp.json";
const ENV_FILES = "sudo find /var/lib/ocel -maxdepth 2 -type f -name '*.env'";
const DECOY = "ocel-journey-decoy";
const DECOY_DATA = "/var/lib/ocel-journey-decoy";
const DECOY_RUN_IMAGE = "mirror.gcr.io/library/busybox:1.37";
const DECOY_PULLED_IMAGE = "mirror.gcr.io/library/alpine:3.20";
const PLANT_DECOY = [
  `sudo docker rm -f ${DECOY} >/dev/null 2>&1 || true`,
  `sudo install -d -m 0755 ${DECOY_DATA}`,
  `echo 'a file ocel never wrote' | sudo tee ${DECOY_DATA}/data >/dev/null`,
  `sudo docker pull -q ${DECOY_PULLED_IMAGE} >/dev/null`,
  `sudo docker run -d --name ${DECOY} -v ${DECOY_DATA}:/data ${DECOY_RUN_IMAGE} sleep 86400 >/dev/null`,
].join(" && ");
const DECOY_FINGERPRINT = [
  `sudo docker inspect -f 'container {{.Id}} {{.State.Status}}' ${DECOY} 2>&1`,
  `sudo docker image inspect -f 'image {{.Id}}' ${DECOY_PULLED_IMAGE} 2>&1`,
  `sudo sha256sum ${DECOY_DATA}/data 2>&1`,
  "true",
].join("; ");
const LEFT_BEHIND = [
  ...OCEL_PATHS.map(heldProbe),
  `getent passwd ${DEPLOY_LOGIN} >/dev/null && echo 'the login ${DEPLOY_LOGIN}'`,
  ...OCEL_CONTAINERS.map(
    (named) => `sudo docker inspect ${named} >/dev/null 2>&1 && echo 'the container ${named}'`,
  ),
  ...OCEL_LABELS.map(
    (label) => `sudo docker ps -a --filter label=${label} --format 'the container {{.Names}}'`,
  ),
  "true",
].join("; ");

const BRING_A_BOX_UP = [
  "scripts/incus.sh create <name>",
  'eval "$(scripts/incus.sh info <name>)"',
  "export OCEL_VPS_HOST=$OCEL_INCUS_ADDR OCEL_VPS_USER=$OCEL_INCUS_USER OCEL_VPS_IDENTITY_FILE=$OCEL_INCUS_KEY",
].join("\n  ");

export type Box = { host: string; user: string; identityFile: string };

type BoxSession = { dir: string; gateway: Gateway; env: NodeJS.ProcessEnv; said: string[] };

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

export function appOrigin(hostname: string, scheme: Scheme): string {
  return `${scheme}://${hostname}`;
}

export function hostnamesWithoutUrl(said: string, hostnames: string[]): string[] {
  return hostnames.filter(
    (hostname) => !new RegExp(`https://${hostname.replaceAll(".", "\\.")}(?![\\w.-])`).test(said),
  );
}

export function entryFile(slug: string): string {
  const encoded = Array.from(Buffer.from(slug, "utf8"), (byte, at) => {
    const char = String.fromCharCode(byte);
    const plain = /^[A-Za-z0-9\-_.]$/.test(char) && !(at === 0 && char === ".");
    return plain ? char : `%${byte.toString(16).toUpperCase().padStart(2, "0")}`;
  }).join("");
  return `${encoded}${ENTRY_SUFFIX}`;
}

export function heldProbe(held: string): string {
  return `sudo test -e '${held}' && echo "${held}$(sudo find '${held}' -mindepth 1 -maxdepth 3 -printf ' %P' 2>/dev/null | head -c 400)"`;
}

export function projectLeftovers(slug: string): string {
  const filter = `--filter label=ocel.project=${slug}`;
  return [
    `sudo docker ps -a ${filter} --format 'the container {{.Names}}'`,
    `sudo docker volume ls ${filter} --format 'the volume {{.Name}}'`,
  ].join("; ");
}

export function resourceRestart(slug: string): string {
  return `sudo docker ps -q --filter label=ocel.project=${slug} --filter label=ocel.resource | xargs -r sudo docker restart`;
}

export function projectExposure(slug: string): string {
  return `sudo docker ps -aq --filter label=ocel.project=${slug} | xargs -r sudo docker inspect; ps -eo args`;
}

export function projectListing(prefix: string): string {
  return `test -d '${KEYVALUES_TIER}' && { ls -1d '${PROJECT_ENTRIES}'/${prefix}*${ENTRY_SUFFIX} 2>/dev/null || true; } || echo ${NO_KEYVALUES_TIER}`;
}

export function slugsOf(listing: string): string[] {
  if (listing.trim() === NO_KEYVALUES_TIER) {
    throw new Error(
      `${KEYVALUES_TIER} does not exist on the box, so nothing here can tell a box that has ` +
        "no harness project from one this listing failed to read",
    );
  }
  return listing
    .split("\n")
    .map((line) => line.trim().split("/").pop() ?? "")
    .filter((name) => name.endsWith(ENTRY_SUFFIX))
    .map((name) => decodeURIComponent(name.slice(0, -ENTRY_SUFFIX.length)));
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

function sshArgv(target: Box, login: string, command: string): string[] {
  return [
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
  ];
}

export async function ssh(target: Box, login: string, command: string): Promise<string> {
  try {
    const { stdout } = await ran("ssh", sshArgv(target, login, command));
    return stdout;
  } catch (error) {
    throw sshRefusal(target, login, command, error);
  }
}

export function sshFed(
  target: Box,
  login: string,
  command: string,
  stdin: string,
): Promise<string> {
  return new Promise((resolve, reject) => {
    const child = spawn("ssh", sshArgv(target, login, command));
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => {
      stdout += chunk;
    });
    child.stderr.on("data", (chunk) => {
      stderr += chunk;
    });
    child.on("error", (error) => reject(sshRefusal(target, login, command, error)));
    child.on("close", (code) => {
      if (code === 0) {
        resolve(stdout);
        return;
      }
      reject(sshRefusal(target, login, command, { code: code ?? "a signal", stderr }));
    });
    child.stdin.end(stdin);
  });
}

type Stamp = { state?: string; seal?: unknown; digests?: unknown };

export function stampRewritten(before: string, after: string): string | undefined {
  const was = JSON.parse(before) as Stamp;
  const now = JSON.parse(after) as Stamp;
  const missed: string[] = [];
  if (now.state !== "complete") {
    missed.push(`it reads state ${JSON.stringify(now.state)}, want "complete"`);
  }
  if (JSON.stringify(now.seal) !== JSON.stringify(was.seal)) {
    missed.push(
      `it names the seal ${JSON.stringify(now.seal)} over ${JSON.stringify(was.seal)}, and a key nothing asked to rotate takes every value sealed under the old one`,
    );
  }
  if (JSON.stringify(now.digests) !== JSON.stringify(was.digests)) {
    missed.push(
      `its digests read ${JSON.stringify(now.digests)} where they read ${JSON.stringify(was.digests)}, so the re-apply rewrote the host`,
    );
  }
  if (missed.length === 0) {
    return undefined;
  }
  return `the stamp a re-apply left at ${STAMP}: ${missed.join("; ")}`;
}

export class VpsTarget implements Target, ReleaseCycle, Restart, Exposure {
  readonly name = "vps";
  readonly workers = 2;
  readonly maxRequestBodyBytes = UNCAPPED_BODY_BYTES;
  readonly stepTimeoutMs = 600_000;

  private readonly sessions = new Map<string, BoxSession>();
  private resolvedBox: Box | undefined;
  private resolvedZone: string | undefined;
  private resolvedFront: { front: Front | undefined } | undefined;
  private decoy: string | undefined;

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
    const front = this.front();
    if (front) {
      const said = await sshFed(
        target,
        target.user,
        stepCommand(coveredNames(BOX_ZONE, this.zone())),
        frontStep(front, "up.sh"),
      );
      const log = path.join(BOOTSTRAP_DIR, `${front.name}-up.log`);
      await mkdir(path.dirname(log), { recursive: true });
      await writeFile(log, redact(said), "utf8");
      await this.refusedUnfronted(front);
    }
    const dir = await this.bootstrapConfig();
    const env = this.boxEnv(target.user);
    const fresh = unbootstrappedMissed(await this.said(dir, "doctor-fresh.log", ["doctor"], env));
    if (fresh !== undefined) {
      throw new Error(
        `${fresh}\nThe lane bootstraps a box nothing has bootstrapped yet. Bring a new one up:\n  ${BRING_A_BOX_UP}`,
      );
    }
    const log = await this.said(dir, "bootstrap.log", ["bootstrap", "production", "--yes"], env);
    const series = engineSeries(process.env);
    const missed = series === undefined ? undefined : adoptionMissed(log, series);
    if (missed !== undefined) {
      throw new Error(missed);
    }
    const doctored = bootstrappedMissed(
      await this.said(dir, "doctor.log", ["doctor"], env),
      front === undefined,
    );
    if (doctored !== undefined) {
      throw new Error(doctored);
    }
    const stamped = await ssh(target, target.user, `sudo cat ${STAMP}`);
    for (const [name, args] of [
      ["replan.log", ["bootstrap", "production", "--dry", "--log-format", "json"]],
      ["reapply.log", ["bootstrap", "production", "--yes", "--log-format", "json"]],
    ] as const) {
      const writes = plannedWrites(await this.said(dir, name, [...args], env));
      if (writes.length > 0) {
        throw new Error(
          `\`ocel ${args.join(" ")}\` right after an apply plans to write ${writes.join(", ")}, and a box bootstrap just wrote has nothing left to write`,
        );
      }
    }
    const rewritten = stampRewritten(stamped, await ssh(target, target.user, `sudo cat ${STAMP}`));
    if (rewritten !== undefined) {
      throw new Error(rewritten);
    }
    await ssh(target, target.user, PLANT_DECOY);
    this.decoy = await ssh(target, target.user, DECOY_FINGERPRINT);
    return {};
  }

  private async refusedUnfronted(front: Front): Promise<void> {
    const target = this.box();
    const dir = await this.boxConfig(
      path.join(BOOTSTRAP_DIR, "unfronted"),
      BOOTSTRAP_SLUG,
      target.user,
      undefined,
    );
    const result = await spawnOcel(
      dir,
      ["bootstrap", "production", "--yes"],
      this.boxEnv(target.user),
    );
    const said = redact(`${result.stdout}${result.stderr}`);
    await writeFile(path.join(dir, "bootstrap.log"), said, "utf8");
    const missed = refusalMissed(result.code, said, ownerOf(front));
    if (missed !== undefined) {
      throw new Error(missed);
    }
  }

  async prepareProcess(): Promise<void> {
    await this.detectLane();
  }

  async finishLane(): Promise<void> {
    const refusals: string[] = [];
    for (const step of [() => this.givenBack(), () => this.frontUntouched()]) {
      await step().catch((error: unknown) => {
        refusals.push(error instanceof Error ? error.message : String(error));
      });
    }
    if (refusals.length > 0) {
      throw new Error(refusals.join("\n\n"));
    }
  }

  private async givenBack(): Promise<void> {
    if ((await this.recordedSlugs()).length > 0) {
      return;
    }
    const target = this.box();
    const envFiles = (await ssh(target, target.user, ENV_FILES)).trim();
    if (envFiles !== "") {
      throw new Error(
        `${envFiles.split("\n").join(", ")} outlived every deploy on the box, and an env file keeps every value its deploy resolved in plaintext`,
      );
    }
    const dir = await this.bootstrapConfig();
    const env = this.boxEnv(target.user);
    const removed = await this.said(
      dir,
      "bootstrap-destroy.log",
      ["bootstrap", "destroy", "production", "--yes"],
      env,
    );
    const unsaid = [BOOTSTRAP_REMOVED, KNOWN_HOSTS_REMEDY].filter(
      (want) => !removed.includes(want),
    );
    if (unsaid.length > 0) {
      throw new Error(
        `\`ocel bootstrap destroy production --yes\` never said ${unsaid.map((want) => `"${want}"`).join(" or ")}:\n${removed}`,
      );
    }
    const doctored = unbootstrappedMissed(
      await this.said(dir, "doctor-destroyed.log", ["doctor"], env),
    );
    if (doctored !== undefined) {
      throw new Error(doctored);
    }
    const said = await ssh(target, target.user, LEFT_BEHIND);
    const left = [...new Set(said.split("\n").map((line) => line.trim()))].filter(Boolean);
    if (left.length > 0) {
      throw new Error(
        `the box still holds ${left.join(", ")} after \`ocel bootstrap destroy production\`, so the machine was not given back`,
      );
    }
    const decoy = await ssh(target, target.user, DECOY_FINGERPRINT);
    if (this.decoy !== undefined && decoy !== this.decoy) {
      throw new Error(
        `\`ocel bootstrap destroy production\` changed what the box ran and kept beside ocel, which is not ocel's to take:\nbefore:\n${this.decoy}\nafter:\n${decoy}`,
      );
    }
    const engine = (await ssh(target, target.user, "systemctl is-active docker || true")).trim();
    if (engine !== "active") {
      throw new Error(
        `docker reads ${engine} after \`ocel bootstrap destroy production\`, and removing ocel from a box leaves the engine everything else on it runs on`,
      );
    }
  }

  private async frontUntouched(): Promise<void> {
    const front = this.front();
    if (!front) {
      return;
    }
    const target = this.box();
    await sshFed(target, target.user, stepCommand([]), frontStep(front, "check.sh"));
  }

  private async said(
    dir: string,
    log: string,
    args: string[],
    env: NodeJS.ProcessEnv,
  ): Promise<string> {
    const result = await spawnOcel(dir, args, env);
    const said = redact(`${result.stdout}\n${result.stderr}`);
    await writeFile(path.join(dir, log), said, "utf8");
    if (result.code !== 0) {
      throw new NonzeroExitError(args, result);
    }
    return said;
  }

  private bootstrapConfig(): Promise<string> {
    return this.boxConfig(BOOTSTRAP_DIR, BOOTSTRAP_SLUG, this.box().user, this.front()?.proxy);
  }

  async deploy(cell: CellUnderTest): Promise<Deployment> {
    const session = await this.sessionFor(cell);
    const drive = this.driving(cell, session, "deploy");

    if (setsEnv(cell.fixture.checks)) {
      await drive("env-greeting", ["env", "set", `GREETING=${INITIAL_GREETING}`]);
    }
    if (setsSecret(cell.fixture.checks)) {
      await drive("env-secret", ["env", "set", `SECRET_TOKEN=${SECRET_TOKEN}`]);
    }
    if (setsJourneyNonce(cell.fixture.checks)) {
      await drive("env-journey-nonce", ["env", "set", `${JOURNEY_NONCE_ENV}=${cell.journeyNonce}`]);
    }
    const deployed = await drive("deploy", ["deploy", "--yes"]);
    const transcript = `${deployed.stdout}\n${deployed.stderr}`;
    if (setsSecret(cell.fixture.checks) && transcript.includes(SECRET_TOKEN)) {
      throw new Error(
        "the deploy printed the value SECRET_TOKEN was set to, and its transcript is what a ci log keeps",
      );
    }
    const pending = hostnamesWithoutUrl(transcript, [...this.hostnamesOf(cell).values()]);
    if (pending.length > 0) {
      throw new Error(
        `the deploy printed no url for ${pending.join(", ")}, so it left a declared hostname ` +
          "pending instead of attaching it",
      );
    }
    if (migrates(cell.fixture.checks)) {
      await this.migrateInPlace(cell);
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

  async restart(cell: CellUnderTest): Promise<Deployment> {
    const session = await this.sessionFor(cell);
    const target = this.box();
    const restarted = await ssh(target, target.user, resourceRestart(cell.slug));
    await cell.evidence.write("restart", "restarted.txt", restarted);
    if (restarted.trim() === "") {
      throw new Error(
        `no container on the box is labelled ocel.project=${cell.slug} and ocel.resource, so nothing ${cell.name} declared was restarted`,
      );
    }
    return this.deployment(cell, session);
  }

  async readExposed(cell: CellUnderTest): Promise<string> {
    const session = await this.sessionFor(cell);
    const target = this.box();
    const shown = await ssh(target, target.user, projectExposure(cell.slug));
    return [...session.said, shown].join("\n");
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
    const target = this.box();
    const said = await ssh(target, target.user, projectLeftovers(cell.slug));
    const left = said
      .split("\n")
      .map((line) => line.trim())
      .filter(Boolean);
    if (left.length > 0) {
      throw new Error(
        `the box still holds ${left.join(", ")} after \`ocel destroy production\` took ${cell.slug} down, and a destroy reclaims everything the project's deploys wrote`,
      );
    }
  }

  private box(): Box {
    this.resolvedBox ??= readBox();
    return this.resolvedBox;
  }

  private front(): Front | undefined {
    this.resolvedFront ??= { front: frontNamed(process.env) };
    return this.resolvedFront.front;
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

  private async boxConfig(
    dir: string,
    slug: string,
    login: string,
    proxy: unknown,
  ): Promise<string> {
    const target = this.box();
    await mkdir(dir, { recursive: true });
    await writeFile(
      path.join(dir, "ocel.json"),
      `${JSON.stringify(
        {
          slug,
          provider: {
            vps: {
              ssh: { host: target.host, user: login, identityFile: target.identityFile },
              ...(proxy === undefined ? {} : { proxy }),
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

  private async migrateInPlace(cell: CellUnderTest): Promise<void> {
    const target = this.box();
    const app = cell.fixture.apps[0];
    if (!app) {
      throw new Error(`${cell.fixture.name} names no app to migrate its database from`);
    }
    const named = await ssh(
      target,
      target.user,
      `sudo docker ps --filter label=ocel.project=${cell.slug} --filter label=ocel.app=${app} ` +
        "--format '{{.Names}}'",
    );
    const container = named.trim().split("\n")[0] ?? "";
    if (container === "") {
      throw new Error(
        `no container on the box is labelled ocel.project=${cell.slug} and ocel.app=${app}, and a ` +
          "deployed database is migrated from the release that binds it",
      );
    }
    const said = await ssh(
      target,
      target.user,
      `sudo docker exec -e OCEL_LIVE_DIR=${CONTAINER_LIVE_DIR} ` +
        `-w ${CONTAINER_APP_ROOT}/${fixtureMember(cell.fixture.name)} ${container} ` +
        migrateCommand().join(" "),
    );
    await cell.evidence.write("deploy", "migrate.log", redact(said));
  }

  private hostnamesOf(cell: CellUnderTest): Map<string, string> {
    return new Map(
      cell.fixture.apps.map((app) => {
        const zone = vpsZoneOf(cell, process.env);
        const hostname = appHostname(app, cell.slug, zone);
        if (!hostname) {
          throw new Error(`${app} has no hostname on ${zone}`);
        }
        return [app, hostname];
      }),
    );
  }

  private async deployment(cell: CellUnderTest, session: BoxSession): Promise<Deployment> {
    const proxied = cloudflareUrls(cell, this.zone());
    const urls = proxied ?? new Map<string, string>();
    for (const [app, hostname] of proxied ? [] : this.hostnamesOf(cell)) {
      await session.gateway.serving(hostname);
      urls.set(app, appOrigin(hostname, schemeOf(this.front())));
    }
    await cell.evidence.write(
      "deploy",
      "deployment.json",
      `${JSON.stringify({ slug: cell.slug, zone: vpsZoneOf(cell, process.env), apps: Object.fromEntries(urls) }, null, 2)}\n`,
    );
    return {
      baseUrl: (app) => {
        const url = urls.get(app);
        if (!url) {
          throw new Error(`${cell.name} has no app named ${app} on vps`);
        }
        return url;
      },
      fetch: proxied
        ? (...args) => fetch(...args)
        : (input, init) => this.reaching(session, input, init),
      ...(proxied ? {} : { reach: (url: string) => this.reachingUrl(session, url) }),
    };
  }

  private async reachingUrl(session: BoxSession, asked: string): Promise<string> {
    const url = new URL(asked);
    if (url.hostname === BOX_ZONE || !url.hostname.endsWith(`.${BOX_ZONE}`)) {
      return asked;
    }
    const isSocket = url.protocol === "ws:" || url.protocol === "wss:";
    const front = new URL(
      await (isSocket
        ? session.gateway.socketServing(url.hostname)
        : session.gateway.serving(url.hostname)),
    );
    url.protocol = front.protocol;
    url.host = front.host;
    return url.toString();
  }

  private async reaching(
    session: BoxSession,
    input: string | URL | Request,
    init?: RequestInit,
  ): Promise<Response> {
    const asked = typeof input === "string" || input instanceof URL ? input.toString() : input.url;
    const url = new URL(asked);
    if (url.hostname === BOX_ZONE || !url.hostname.endsWith(`.${BOX_ZONE}`)) {
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
      gateway: openGateway(this.box().host, schemeOf(this.front())),
      env: this.boxEnv(DEPLOY_LOGIN),
      said: [],
    };
    this.sessions.set(cell.slug, session);
    return session;
  }

  private driving(cell: CellUnderTest, session: BoxSession, phase: Phase) {
    return (name: string, args: string[]) =>
      recordOutput(
        session.said,
        runOcel(
          cell,
          session.dir,
          phase,
          name,
          ["--config", journeyConfigIn(session.dir), ...args],
          session.env,
        ),
      );
  }

  private async stillRecorded(slug: string): Promise<boolean> {
    const said = await ssh(
      this.box(),
      DEPLOY_LOGIN,
      `test -e '${PROJECT_ENTRIES}/${entryFile(slug)}' && echo present || echo gone`,
    );
    return said.trim() === "present";
  }

  private async recordedSlugs(): Promise<string[]> {
    return slugsOf(await ssh(this.box(), DEPLOY_LOGIN, projectListing(HARNESS_PREFIX)));
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
        this.front()?.proxy,
      );
      await ocel(dir, ["destroy", "production", "--yes"], this.boxEnv(DEPLOY_LOGIN));
    }
  }
}
