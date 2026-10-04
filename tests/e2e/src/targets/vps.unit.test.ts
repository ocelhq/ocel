import { describe, expect, it } from "bun:test";
import { REDACTED } from "../checks/context";
import {
  appOrigin,
  boxLane,
  entryFile,
  heldProbe,
  hostnamesWithoutUrl,
  projectExposure,
  projectLeftovers,
  projectListing,
  resourceRestart,
  slugsOf,
  ssh,
  stampRewritten,
} from "./vps";

const IDENTITY = "/nonexistent/ocel-journey-identity";

describe("boxLane", () => {
  it("reads the incus marker as the emulator lane", () => {
    expect(boxLane("incus\n")).toBe("vps.incus");
  });

  it("reads a box without the marker as a real one", () => {
    expect(boxLane("real\n")).toBe("vps");
  });

  it("refuses an answer it cannot read rather than guessing a box is disposable", () => {
    expect(() => boxLane("")).toThrow(/whether it runs under incus/);
  });
});

describe("appOrigin", () => {
  it("names the app's own hostname behind the box edge, never the forwarder's loopback port", () => {
    expect(appOrigin("web-j-1-deploy-node.localhost", "http")).toBe(
      "http://web-j-1-deploy-node.localhost",
    );
  });

  it("names it over https behind a front that serves the app only over https", () => {
    expect(appOrigin("web-j-1-deploy-node.localhost", "https")).toBe(
      "https://web-j-1-deploy-node.localhost",
    );
  });
});

describe("hostnamesWithoutUrl", () => {
  it("passes a deploy that printed a url for every hostname it declares", () => {
    const said = "Done in 1m\n\n    web  https://web-j-1-deploy-python.localhost\n";
    expect(hostnamesWithoutUrl(said, ["web-j-1-deploy-python.localhost"])).toEqual([]);
  });

  it("names the hostnames the deploy left pending", () => {
    const said =
      "Done in 2m\n\n    web-j-1-deploy-python.localhost does not answer through the origin yet — `ocel domain add` picks up where it stopped\n";
    expect(hostnamesWithoutUrl(said, ["web-j-1-deploy-python.localhost"])).toEqual([
      "web-j-1-deploy-python.localhost",
    ]);
  });

  it("does not read one hostname's url as another's that it prefixes", () => {
    const said = "    web  https://web.localhost.example\n";
    expect(hostnamesWithoutUrl(said, ["web.localhost"])).toEqual(["web.localhost"]);
  });
});

describe("entryFile", () => {
  it("names the entry a slug of plain characters is kept in", () => {
    expect(entryFile("j-local-ada-node")).toBe("j-local-ada-node.json");
  });

  it("percent-encodes what the key-value tier cannot keep in a file name", () => {
    expect(entryFile("j-local-ada/node")).toBe("j-local-ada%2Fnode.json");
  });

  it("encodes a leading dot so no entry hides itself", () => {
    expect(entryFile(".hidden")).toBe("%2Ehidden.json");
  });
});

describe("slugsOf", () => {
  it("names one project per entry the box stores", () => {
    expect(slugsOf("/keyvalues/projects/j-local-ada-node.json\n")).toEqual(["j-local-ada-node"]);
  });

  it("ignores anything that is not an entry", () => {
    expect(slugsOf("\nno-entries-here\n")).toEqual([]);
  });

  it("reads back a slug the key-value tier percent-encoded", () => {
    expect(slugsOf("j-local-ada%2Fnode.json")).toEqual(["j-local-ada/node"]);
  });

  it("refuses to read a box with no key-value tier as a box storing nothing", () => {
    expect(() => slugsOf("no-keyvalues-tier\n")).toThrow(
      /\/var\/lib\/ocel\/production\/keyvalues does not exist/,
    );
  });
});

describe("heldProbe", () => {
  it("names what a path ocel should have removed still holds", () => {
    expect(heldProbe("/var/lib/ocel")).toBe(
      "sudo test -e '/var/lib/ocel' && echo \"/var/lib/ocel$(sudo find '/var/lib/ocel' -mindepth 1 -maxdepth 3 -printf ' %P' 2>/dev/null | head -c 400)\"",
    );
  });
});

describe("projectLeftovers", () => {
  it("names every container and volume still labelled with the project", () => {
    expect(projectLeftovers("j-kv")).toBe(
      "sudo docker ps -a --filter label=ocel.project=j-kv --format 'the container {{.Names}}'; " +
        "sudo docker volume ls --filter label=ocel.project=j-kv --format 'the volume {{.Name}}'",
    );
  });
});

describe("resourceRestart", () => {
  it("restarts only the containers the project's resources run in, naming each", () => {
    expect(resourceRestart("j-kv")).toBe(
      "sudo docker ps -q --filter label=ocel.project=j-kv --filter label=ocel.resource " +
        "| xargs -r sudo docker restart",
    );
  });
});

describe("projectExposure", () => {
  it("shows the spec of every container labelled with the project, and the box's process table", () => {
    expect(projectExposure("j-kv")).toBe(
      "sudo docker ps -aq --filter label=ocel.project=j-kv | xargs -r sudo docker inspect; " +
        "ps -eo args",
    );
  });
});

describe("projectListing", () => {
  it("asks whether the key-value tier exists, since forgetting the last project prunes its directory", () => {
    expect(projectListing("j-")).toStartWith("test -d '/var/lib/ocel/production/keyvalues' &&");
  });

  it("lists the harness projects' entries under the projects partition", () => {
    expect(projectListing("j-")).toContain(
      "ls -1d '/var/lib/ocel/production/keyvalues/projects'/j-*.json",
    );
  });
});

describe("stampRewritten", () => {
  const stamp = (fingerprint: string, digest: string, state = "complete") =>
    JSON.stringify({
      schema: 3,
      state,
      writer: "ocel dev",
      seal: { fingerprint, algorithm: "age-x25519", createdAt: "2026-09-28T10:00:00Z" },
      digests: { host: digest },
    });

  it("passes a re-apply that leaves the seal and every digest as the apply wrote them", () => {
    expect(stampRewritten(stamp("SHA256:a", "d1"), stamp("SHA256:a", "d1"))).toBeUndefined();
  });

  it("names a seal the re-apply minted over the one the apply wrote", () => {
    expect(stampRewritten(stamp("SHA256:a", "d1"), stamp("SHA256:b", "d1"))).toContain("SHA256:b");
  });

  it("names a digest the re-apply rewrote", () => {
    expect(stampRewritten(stamp("SHA256:a", "d1"), stamp("SHA256:a", "d2"))).toContain(
      "rewrote the host",
    );
  });

  it("names a stamp the re-apply left unfinished", () => {
    expect(stampRewritten(stamp("SHA256:a", "d1"), stamp("SHA256:a", "d1", "applying"))).toContain(
      '"applying"',
    );
  });
});

describe("ssh", () => {
  it("names no identity file in what it throws when the box does not answer", async () => {
    const thrown = await ssh(
      { host: "box.invalid", user: "nobody", identityFile: IDENTITY },
      "nobody",
      "true",
    ).then(
      () => new Error("the box answered, and nothing here can reach box.invalid"),
      (error: Error) => error,
    );
    expect(thrown.message).not.toContain(IDENTITY);
    expect(thrown.message).toContain(REDACTED);
    expect(thrown.message).toContain("nobody@box.invalid");
  });
});
