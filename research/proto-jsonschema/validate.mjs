// Validates real and mutated protojson lines against each candidate plugin's
// generated JSON Schema, with ajv (draft 2020-12, or draft-04 for chrusty) and
// ajv-formats asserting `format`.
//
//   VALIDATOR_DIR=/path/with/node_modules node research/proto-jsonschema/validate.mjs
//
// VALIDATOR_DIR holds ajv@8.17.1, ajv-formats@3.0.1 and ajv-draft-04@1.0.0.
import { readFileSync, readdirSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const require = createRequire(join(process.env.VALIDATOR_DIR ?? "/tmp/ocel-research/validator", "x.js"));
const Ajv2020 = require("ajv/dist/2020").default;
const AjvDraft04 = require("ajv-draft-04").default;
const addFormats = require("ajv-formats").default;

// buf.validate patterns are RE2; JSON Schema patterns are ECMA-262. The repo's
// protos use RE2 POSIX classes, which no plugin translates. REWRITE=0 compiles
// the generated schemas as they are; the default rewrites the two classes the
// protos use into their RE2-documented ASCII ranges, as a proto fix would.
const rewrite = process.env.REWRITE !== "0";
const ecma = (node) => {
  if (Array.isArray(node)) return node.map(ecma);
  if (!node || typeof node !== "object") return node;
  return Object.fromEntries(
    Object.entries(node).map(([key, value]) => [
      key,
      key === "pattern" && typeof value === "string"
        ? value.replaceAll("[:cntrl:]", "\\x00-\\x1f\\x7f").replaceAll("[:space:]", " \\t\\n\\v\\f\\r")
        : ecma(value),
    ]),
  );
};
const read = (path) => {
  const parsed = JSON.parse(readFileSync(join(here, path), "utf8"));
  return rewrite ? ecma(parsed) : parsed;
};
const lines = (path) =>
  readFileSync(join(here, path), "utf8")
    .split("\n")
    .filter(Boolean)
    .map((line) => JSON.parse(line));

function ajvFor(draft) {
  const opts = { strict: false, allErrors: true, validateFormats: process.env.FORMATS !== "annotate" };
  const ajv = draft === "04" ? new AjvDraft04(opts) : new Ajv2020(opts);
  addFormats(ajv);
  for (const name of ["int64", "int32", "uint64", "byte", "binary", "double", "float"]) {
    ajv.addFormat(name, true);
  }
  return ajv;
}

function protoschemaValidator(dir, root, variant) {
  return ajvFor("2020").compile(read(`gen/${dir}/${root}.jsonschema.${variant}.json`));
}

function connectOpenAPIValidator(file, root) {
  const doc = read(file);
  return ajvFor("2020").compile({ ...doc, $ref: `#/$defs/${root}` });
}

// Diagnostic only: drops every `required` so a probe row shows whether the
// plugin types that one field right, not that it demands every other field.
const unrequired = (node) => {
  if (Array.isArray(node)) return node.map(unrequired);
  if (!node || typeof node !== "object") return node;
  return Object.fromEntries(
    Object.entries(node)
      .filter(([key, value]) => !(key === "required" && Array.isArray(value) && node.properties))
      .map(([key, value]) => [key, unrequired(value)]),
  );
};

function pubgValidator(file, strip = false) {
  const schema = read(file);
  return ajvFor("2020").compile(strip ? unrequired(schema) : schema);
}

function chrustyValidator(dir, root, strip = false) {
  const ajv = ajvFor("04");
  const load = (name) => (strip ? unrequired(read(`${dir}/${name}`)) : read(`${dir}/${name}`));
  for (const name of readdirSync(join(here, dir))) {
    if (name !== `${root}.json`) ajv.addSchema(load(name), name);
  }
  return ajv.compile(load(`${root}.json`));
}

const stream = {
  "protoschema json (lenient)": () => protoschemaValidator("protoschema", "cli.stream.v1.RunEvent", "bundle"),
  "protoschema json-strict": () => protoschemaValidator("protoschema", "cli.stream.v1.RunEvent", "strict.bundle"),
  "connect-openapi format=jsonschema": () =>
    connectOpenAPIValidator("gen/connect-openapi/ocel.cli.jsonschema.json", "cli.stream.v1.RunEvent"),
  "connect-openapi, protovalidate off": () =>
    connectOpenAPIValidator("gen/connect-openapi-noprotovalidate/ocel.cli.jsonschema.json", "cli.stream.v1.RunEvent"),
  "pubg (int64+presence)": () => pubgValidator("gen/pubg/cli/stream/v1/stream.schema.json"),
  "pubg (int64 only)": () => pubgValidator("gen/pubg-default/cli/stream/v1/stream.schema.json"),
  "chrusty (archived, draft-04)": () => chrustyValidator("gen/chrusty/cli.stream.v1", "RunEvent"),
};

const probe = {
  "protoschema json (lenient)": () => protoschemaValidator("probe/protoschema", "probe.v1.Probe", "bundle"),
  "protoschema json-strict": () => protoschemaValidator("probe/protoschema", "probe.v1.Probe", "strict.bundle"),
  "connect-openapi format=jsonschema": () =>
    connectOpenAPIValidator("gen/probe/connect-openapi/probe.jsonschema.json", "probe.v1.Probe"),
  "pubg (int64+presence)": () => pubgValidator("gen/probe/pubg/probe/v1/probe.schema.json"),
  "pubg (int64 only)": () => pubgValidator("gen/probe/pubg-default/probe/v1/probe.schema.json"),
  "chrusty (archived, draft-04)": () => chrustyValidator("gen/probe/chrusty/probe.v1", "Probe"),
  "pubg (int64+presence), required dropped": () => pubgValidator("gen/probe/pubg/probe/v1/probe.schema.json", true),
  "chrusty, required dropped": () => chrustyValidator("gen/probe/chrusty/probe.v1", "Probe", true),
};

const realFailed = lines("samples/deploy-vps.ndjson");
const realSummary = realFailed.at(-1);
const sink = lines("samples/sink-populated.ndjson");
const populated = sink[1];
const clone = (value) => structuredClone(value);
const renamed = (obj, from, to) =>
  Object.fromEntries(Object.entries(obj).map(([key, value]) => [key === from ? to : key, value]));
const withSummary = (base, change) => {
  const ev = clone(base);
  ev.summary = change(ev.summary);
  return ev;
};

// expect: what protojson itself does with the line. "emit" lines are protojson
// output, which every schema must accept; "reject" lines protojson.Unmarshal
// refuses; "parse" lines protojson.Unmarshal accepts but never emits.
const streamCases = [
  ...realFailed.map((ev, i) => ({ name: `real deploy line ${i + 1}${i === realFailed.length - 1 ? " (summary)" : ""}`, ev, expect: "emit" })),
  ...sink.map((ev, i) => ({ name: `sink line ${i + 1}${i === 1 ? " (populated summary)" : ""}`, ev, expect: "emit" })),
  { name: "summary.durationMs misspelled durationMS", ev: withSummary(realSummary, (s) => renamed(s, "durationMs", "durationMS")), expect: "reject" },
  { name: "summary.headline misspelled headLine", ev: withSummary(realSummary, (s) => renamed(s, "headline", "headLine")), expect: "reject" },
  { name: "summary.origin.vendor misspelled vendr", ev: withSummary(populated, (s) => ({ ...s, origin: renamed(s.origin, "vendor", "vendr") })), expect: "reject" },
  { name: "summary.tier unknown enum name", ev: withSummary(populated, (s) => ({ ...s, tier: "TIER_BOGUS" })), expect: "reject" },
  { name: "summary.durationMs not an integer", ev: withSummary(realSummary, (s) => ({ ...s, durationMs: "16ms" })), expect: "reject" },
  { name: "oneof cli: summary and resumed both set", ev: { ...clone(realSummary), resumed: { reason: "x" } }, expect: "reject" },
  { name: "operation.time not RFC 3339", ev: { ...clone(realSummary), operation: { ...realSummary.operation, time: "yesterday" } }, expect: "reject" },
  { name: "operation.spanId not base64", ev: { ...clone(realFailed[0]), operation: { ...realFailed[0].operation, spanId: "!!!!!!!!!!!!" } }, expect: "reject" },
  { name: "summary.durationMs as JSON number", ev: withSummary(realSummary, (s) => ({ ...s, durationMs: 16 })), expect: "parse" },
  { name: "summary.tier as enum number", ev: withSummary(populated, (s) => ({ ...s, tier: 2 })), expect: "parse" },
  { name: "summary.duration_ms (proto name)", ev: withSummary(realSummary, (s) => renamed(s, "durationMs", "duration_ms")), expect: "parse" },
];

const canonical = read("samples/probe.json");
const probeInput = read("probe/input.json");
const probeCases = [
  { name: "probe canonical protojson output", ev: canonical, expect: "emit" },
  ...Object.keys(canonical).map((key) => ({ name: `probe output, only ${key}`, ev: { [key]: canonical[key] }, expect: "emit" })),
  { name: "probe parse-only input (names, numbers, enum ints)", ev: probeInput, expect: "parse" },
  { name: "probe oneof: text and inner both set", ev: { ...canonical, text: "t" }, expect: "reject" },
  { name: "probe took as ISO 8601 PT1S", ev: { took: "PT1S" }, expect: "reject" },
  { name: "probe counts value not an integer", ev: { counts: { a: "x" } }, expect: "reject" },
  { name: "probe misspelled field", ev: { ...canonical, snakeCaseNam: "s" }, expect: "reject" },
];

function run(title, validators, cases) {
  console.log(`\n# ${title}\n`);
  const names = Object.keys(validators);
  const compiled = Object.fromEntries(
    names.map((name) => {
      try {
        return [name, validators[name]()];
      } catch (err) {
        return [name, err];
      }
    }),
  );
  console.log(`| case | protojson | ${names.join(" | ")} |`);
  console.log(`|---|---|${names.map(() => "---").join("|")}|`);
  const failures = [];
  for (const { name, ev, expect } of cases) {
    const cells = names.map((plugin) => {
      const validate = compiled[plugin];
      if (validate instanceof Error) return "compile error";
      const ok = validate(ev);
      const right = expect === "reject" ? !ok : expect === "emit" ? ok : true;
      if (!ok) failures.push({ plugin, name, errors: validate.errors.slice(0, 3) });
      return `${ok ? "accept" : "reject"}${right ? "" : " ✗"}`;
    });
    console.log(`| ${name} | ${expect} | ${cells.join(" | ")} |`);
  }
  for (const [name, validate] of Object.entries(compiled)) {
    if (validate instanceof Error) console.log(`\n${name}: compile error: ${validate.message}`);
  }
  console.log(`\n## first errors per rejection (${title})\n`);
  for (const { plugin, name, errors } of failures) {
    console.log(`- ${plugin} / ${name}: ${errors.map((e) => `${e.instancePath || "/"} ${e.message}${e.params?.additionalProperty ? ` (${e.params.additionalProperty})` : ""}`).join("; ")}`);
  }
}

console.log(`formats: ${process.env.FORMATS === "annotate" ? "annotation only (the 2020-12 default)" : "asserted (ajv-formats)"}; RE2 classes rewritten: ${rewrite}`);
run("run-event stream (proto/cli/stream/v1)", stream, streamCases);
run("probe.v1.Probe (encodings matrix)", probe, probeCases);
