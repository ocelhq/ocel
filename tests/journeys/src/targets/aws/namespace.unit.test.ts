import { describe, it } from "bun:test";
import assert from "node:assert/strict";
import { projectSlug } from "../../identity";
import {
  bootstrapStackOf,
  DEFAULT_NAMESPACE,
  LONGEST_NAMESPACE,
  namespaceFor,
  namespaceOf,
  strayNamespaces,
} from "./namespace";

describe("namespaceFor", () => {
  it("names the cell the way its project slug does", () => {
    assert.equal(namespaceFor("deploy-node", "1874"), "j-1874-deploy-node");
    assert.equal(namespaceFor("deploy-node", "1874"), projectSlug("deploy-node", "1874"));
  });

  it("carries a slash in a cell name across as a dash", () => {
    assert.equal(namespaceFor("sdk-node/api-gateway", "1874"), "j-1874-sdk-node-api-gateway");
  });

  it("lowercases what a local run id brings, because AWS names have no upper case", () => {
    assert.equal(namespaceFor("deploy-node", "local-Victor_N"), "j-local-victor-n-deploy-node");
  });

  it("fits the bound, and two long cells still land on different namespaces", () => {
    const one = namespaceFor("a".repeat(60), "local-someone");
    const two = namespaceFor("b".repeat(60), "local-someone");
    assert.ok(one.length <= LONGEST_NAMESPACE, `${one} is ${one.length} characters`);
    assert.ok(two.length <= LONGEST_NAMESPACE, `${two} is ${two.length} characters`);
    assert.notEqual(one, two);
  });

  it("truncates the same name to the same namespace every time", () => {
    assert.equal(
      namespaceFor("a".repeat(60), "local-someone"),
      namespaceFor("a".repeat(60), "local-someone"),
    );
  });

  it("only ever produces a name AWS accepts", () => {
    for (const cell of ["deploy-node", "sdk/workspace", "x".repeat(80)]) {
      const got = namespaceFor(cell, "local-Some_One");
      assert.match(got, /^[a-z][a-z0-9-]*$/, got);
    }
  });
});

describe("namespaceOf", () => {
  it("is ocel when nothing names one", () => {
    assert.equal(namespaceOf({}), DEFAULT_NAMESPACE);
    assert.equal(namespaceOf({ OCEL_NAMESPACE: "" }), DEFAULT_NAMESPACE);
  });

  it("is what OCEL_NAMESPACE names", () => {
    assert.equal(namespaceOf({ OCEL_NAMESPACE: "j-1874-deploy-node" }), "j-1874-deploy-node");
  });
});

describe("bootstrapStackOf", () => {
  it("names the core stack the provider writes under that namespace", () => {
    assert.equal(bootstrapStackOf(DEFAULT_NAMESPACE), "ocel-bootstrap");
    assert.equal(bootstrapStackOf("j-1874-deploy-node"), "j-1874-deploy-node-bootstrap");
  });
});

describe("strayNamespaces", () => {
  const mine = ["j-1874-deploy-node", "j-1874-sdk-node"];

  it("leaves the namespaces this run drives alone", () => {
    assert.deepEqual(strayNamespaces(mine, mine), []);
  });

  it("reclaims a harness namespace an earlier run left standing", () => {
    assert.deepEqual(strayNamespaces([...mine, "j-1799-deploy-node"], mine), [
      "j-1799-deploy-node",
    ]);
  });

  it("leaves anything that is not the harness's alone", () => {
    assert.deepEqual(strayNamespaces(["ocel", "someone-elses"], mine), []);
  });

  it("names a stray once however many stacks carry it", () => {
    assert.deepEqual(strayNamespaces(["j-1799-x", "j-1799-x", "j-1799-x"], mine), ["j-1799-x"]);
  });
});
