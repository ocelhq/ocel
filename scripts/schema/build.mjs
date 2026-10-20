import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { readPublishedSchemas } from "./published-schemas.mjs";

const REPO_ROOT = join(dirname(fileURLToPath(import.meta.url)), "..", "..");

const read = (path) => JSON.parse(readFileSync(path, "utf8"));

const BUNDLE_SUFFIX = ".jsonschema.bundle.json";
const DEFINITION_SUFFIX = ".jsonschema.json";

function providerFragments() {
  const platform = join(REPO_ROOT, "platform");
  return readdirSync(platform, { withFileTypes: true })
    .filter((entry) => entry.isDirectory())
    .map((entry) => join(platform, entry.name, "provider", "schema.provider.json"))
    .filter((path) => {
      try {
        readFileSync(path);
        return true;
      } catch {
        return false;
      }
    })
    .map(read)
    .map(qualified)
    .sort((a, b) => a.id.localeCompare(b.id));
}

function edgeFragments(dir = join(REPO_ROOT, "platform")) {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      return entry.name === "node_modules" || entry.name.startsWith(".") ? [] : edgeFragments(path);
    }
    return entry.name === "schema.edge.json" ? [read(path)] : [];
  });
}

function qualified(fragment) {
  const prefix = fragment.id[0].toUpperCase() + fragment.id.slice(1);
  const rename = (node) => {
    if (!node || typeof node !== "object") return;
    if (Array.isArray(node)) {
      for (const item of node) rename(item);
      return;
    }
    if (typeof node.title === "string" && !node.title.startsWith(prefix)) {
      node.title = prefix + node.title;
    }
    for (const value of Object.values(node)) rename(value);
  };
  rename(fragment.options);
  return fragment;
}

function keyed(selector, entries) {
  const [spelled, object] = selector.oneOf;
  const shorthand = entries.filter(([, options]) => !options.required?.length).map(([id]) => id);
  return {
    ...selector,
    oneOf: [
      { ...spelled, enum: shorthand },
      { ...object, additionalProperties: false, properties: Object.fromEntries(entries) },
    ],
  };
}

function servedBy(ids, selector) {
  const options = selector.oneOf[1].additionalProperties;
  return keyed(
    selector,
    ids.map((id) => [id, options]),
  );
}

function edgeOptions(fragments) {
  const options = new Map(edgeFragments().map((fragment) => [fragment.id, fragment.options]));
  const served = new Set(fragments.flatMap((fragment) => fragment.edges));
  for (const id of served) {
    if (!options.has(id)) {
      throw new Error(
        `a provider fronts deployments with the ${id} edge, and no schema.edge.json under platform/ declares its options`,
      );
    }
  }
  for (const id of options.keys()) {
    if (!served.has(id)) {
      throw new Error(
        `platform/ declares options for the ${id} edge, and no provider fronts deployments with it`,
      );
    }
  }
  return options;
}

function titled(selector, title) {
  return { ...selector, title };
}

function withEdgeAndDNS(fragment, templates, edges) {
  const prefix = fragment.id[0].toUpperCase() + fragment.id.slice(1);
  const edge = keyed(
    templates.edge,
    [...fragment.edges].sort().map((id) => [id, edges.get(id)]),
  );
  const dns = servedBy([...fragment.dns].sort(), templates.dns);
  const options = fragment.options;
  return {
    ...fragment,
    options: {
      ...options,
      properties: {
        ...options.properties,
        edge: titled(edge, `${prefix}EdgeDescriptor`),
        dns: titled(dns, `${prefix}DNSDescriptor`),
      },
    },
  };
}

function schema() {
  const core = read(join(REPO_ROOT, "pkg", "configdoc", "schema.core.json"));
  const { provider } = core.properties;
  const templates = provider.oneOf[1].additionalProperties.properties;
  const edges = edgeOptions(providerFragments());
  const fragments = providerFragments().map((fragment) =>
    withEdgeAndDNS(fragment, templates, edges),
  );
  return {
    ...core,
    properties: {
      ...core.properties,
      provider: keyed(
        provider,
        fragments.map((fragment) => [fragment.id, fragment.options]),
      ),
    },
  };
}

function selectors(merged) {
  const selection = (selector) => ({
    ids: Object.keys(selector.oneOf[1].properties),
    shorthand: selector.oneOf[0].enum,
  });
  const { provider } = merged.properties;
  const options = Object.entries(provider.oneOf[1].properties);
  const each = (key) =>
    Object.fromEntries(options.map(([id, shape]) => [id, selection(shape.properties[key])]));
  return {
    provider: { ...selection(provider), options: textOptions(provider) },
    edges: each("edge"),
    dns: each("dns"),
  };
}

function textOptions(selector) {
  const selectorKeys = new Set(["edge", "dns"]);
  const acceptsText = (node) =>
    node?.type === "string" || (node?.oneOf ?? []).some((one) => one.type === "string");
  return Object.fromEntries(
    Object.entries(selector.oneOf[1].properties)
      .map(([id, options]) => [
        id,
        Object.entries(options.properties ?? {})
          .filter(([name, node]) => !selectorKeys.has(name) && acceptsText(node))
          .map(([name, node]) => ({
            name,
            doc: node.description ?? "",
            ...((options.required ?? []).includes(name) && { required: true }),
          })),
      ])
      .filter(([, listed]) => listed.length > 0),
  );
}

const RESERVED = new Set(["OcelConfig"]);

class Emitter {
  constructor() {
    this.interfaces = new Map();
  }

  type(node, indent = "") {
    if (node === false) return "never";
    if (node.allOf) return this.type(node.allOf[0], indent);
    if (node.const !== undefined) return JSON.stringify(node.const);
    if (node.enum) return node.enum.map((value) => JSON.stringify(value)).join(" | ");
    if (node.oneOf) return this.union(node, indent);
    switch (node.type) {
      case "string":
        return node.examples ? suggested(node.examples) : prefixed(node.pattern);
      case "boolean":
        return "boolean";
      case "integer":
      case "number":
        return "number";
      case "array":
        return `${this.wrapped(node.items, indent)}[]`;
      case "object":
        return this.object(node, indent);
      default:
        return "unknown";
    }
  }

  wrapped(node, indent) {
    const rendered = this.type(node, indent);
    return rendered.includes(" | ") ? `(${rendered})` : rendered;
  }

  union(node, indent) {
    const keys = node.oneOf.filter(soleKey).map((one) => Object.keys(one.properties)[0]);
    const member = (one, at) => (soleKey(one) ? this.keyed(one, keys, at) : this.type(one, at));
    if (!node.title || RESERVED.has(node.title)) {
      return node.oneOf.map((one) => member(one, indent)).join(" | ");
    }
    if (!this.interfaces.has(node.title)) {
      this.interfaces.set(node.title, "");
      const union = node.oneOf.map((one) => member(one, "")).join("\n  | ");
      this.interfaces.set(node.title, `${doc(node)}export type ${node.title} =\n  | ${union};\n`);
    }
    return node.title;
  }

  keyed(node, keys, indent) {
    const inner = `${indent}  `;
    const [key] = Object.keys(node.properties);
    const property = node.properties[key];
    const lines = property.description ? [`${inner}/** ${property.description} */`] : [];
    lines.push(`${inner}${quoted(key)}: ${this.type(property, inner)};`);
    for (const other of keys.filter((each) => each !== key)) {
      lines.push(`${inner}${quoted(other)}?: never;`);
    }
    return `{\n${lines.join("\n")}\n${indent}}`;
  }

  exclusive(node, indent) {
    const inner = `${indent}  `;
    const ids = Object.keys(node.properties);
    return ids
      .map((id) => {
        const lines = ids.map((other) =>
          other === id
            ? `${inner}${quoted(id)}: ${this.type(node.properties[id], inner)};`
            : `${inner}${quoted(other)}?: never;`,
        );
        return `{\n${lines.join("\n")}\n${indent}}`;
      })
      .join(" | ");
  }

  object(node, indent) {
    if (node.maxProperties === 1 && node.properties) return this.exclusive(node, indent);
    if (
      node.properties &&
      Object.keys(node.properties).length === 0 &&
      node.additionalProperties === false
    ) {
      if (!node.title) return "Record<string, never>";
      if (!this.interfaces.has(node.title)) {
        this.interfaces.set(
          node.title,
          `${doc(node)}export type ${node.title} = Record<string, never>;\n`,
        );
      }
      return node.title;
    }
    if (!node.properties) {
      const values = node.additionalProperties;
      return values && values !== true
        ? `Record<string, ${this.type(values, indent)}>`
        : "Record<string, unknown>";
    }
    if (node.title && !RESERVED.has(node.title)) {
      this.declare(node.title, node);
      return node.title;
    }
    return this.body(node, indent);
  }

  body(node, indent) {
    const inner = `${indent}  `;
    const lines = [];
    for (const [key, property] of Object.entries(node.properties)) {
      if (property.description) {
        lines.push(`${inner}/** ${property.description} */`);
      }
      const optional = (node.required ?? []).includes(key) ? "" : "?";
      lines.push(`${inner}${quoted(key)}${optional}: ${this.type(property, inner)};`);
    }
    return `{\n${lines.join("\n")}\n${indent}}`;
  }

  declare(name, node) {
    if (this.interfaces.has(name)) return;
    this.interfaces.set(name, "");
    this.interfaces.set(name, `${doc(node)}export interface ${name} ${this.body(node, "")}\n`);
  }

  render() {
    return [...this.interfaces.values()].join("\n");
  }
}

function soleKey(node) {
  return (
    !node.title &&
    node.type === "object" &&
    node.additionalProperties === false &&
    Object.keys(node.properties ?? {}).length === 1 &&
    node.required?.length === 1
  );
}

function suggested(examples) {
  return [...examples.map((value) => JSON.stringify(value)), "(string & {})"].join(" | ");
}

function prefixed(pattern) {
  const spelled = /^\^((?:[^\\^$.|?*+()[\]{}`]|\\[$.|?*+()[\]{}^\\])+)/.exec(pattern ?? "")?.[1];
  if (!spelled) return "string";
  const prefix = spelled.replace(/\\(.)/g, "$1").replace(/\\/g, "\\\\").replace(/\$\{/g, "\\${");
  return `\`${prefix}\${string}\``;
}

function doc(node) {
  return node.description ? `/** ${node.description} */\n` : "";
}

function quoted(key) {
  return /^[A-Za-z_$][A-Za-z0-9_$]*$/.test(key) ? key : JSON.stringify(key);
}

function types(merged) {
  const emitter = new Emitter();
  const properties = { ...merged.properties };
  delete properties.$schema;
  const config = emitter.body({ ...merged, properties }, "");

  const header = [
    "// generated by scripts/schema/build.mjs from the committed JSON Schema; do not edit",
    "",
    `/** The project configuration \`ocel deploy\` reads, whether written as ${"`ocel.json`"}, as ${"`ocel.yaml`"} or as ${"`ocel.config.ts`"}. */`,
    `export interface OcelConfig ${config}`,
    "",
  ].join("\n");

  return `${header}\n${emitter.render()}`;
}

function generateMessageSchemas() {
  const scratch = mkdtempSync(join(tmpdir(), "ocel-message-schemas-"));
  try {
    execFileSync(
      "pnpm",
      ["exec", "buf", "generate", "--template", "proto/buf.gen.schema.yaml", "--output", scratch],
      { cwd: REPO_ROOT, stdio: "inherit" },
    );
    const oneofs = readOneofs(scratch);
    return readdirSync(scratch)
      .filter((file) => file.endsWith(BUNDLE_SUFFIX))
      .sort()
      .map((file) => {
        const name = file.slice(0, -BUNDLE_SUFFIX.length);
        const bundle = read(join(scratch, file));
        rewriteECMAScriptPatterns(bundle);
        refuseNonECMAScriptPatterns(name, bundle);
        constrainOneofs(name, bundle, oneofs);
        return { name, bundle };
      });
  } finally {
    rmSync(scratch, { recursive: true, force: true });
  }
}

function readOneofs(scratch) {
  const image = join(scratch, "image.json");
  execFileSync("pnpm", ["exec", "buf", "build", "proto", "--output", image], {
    cwd: REPO_ROOT,
    stdio: "inherit",
  });
  const oneofs = new Map();
  const walk = (scope, message) => {
    const name = `${scope}.${message.name}`;
    const members = (message.oneofDecl ?? []).map(() => []);
    for (const field of message.field ?? []) {
      if (field.oneofIndex !== undefined && !field.proto3Optional) {
        members[field.oneofIndex].push(field);
      }
    }
    const declared = members.filter((fields) => fields.length > 0);
    if (declared.length > 0) oneofs.set(name, declared);
    for (const nested of message.nestedType ?? []) walk(name, nested);
  };
  for (const file of read(image).file) {
    for (const message of file.messageType ?? []) walk(file.package, message);
  }
  return oneofs;
}

function constrainOneofs(name, bundle, oneofs) {
  for (const [key, definition] of Object.entries(bundle.$defs ?? {})) {
    const groups = oneofs.get(key.slice(0, -DEFINITION_SUFFIX.length));
    if (!groups || !definition.properties) continue;
    if (definition.allOf) {
      throw new Error(`${name}: ${key} already has an allOf to hold its oneofs`);
    }
    definition.allOf = groups.map((fields) => ({
      oneOf: [{ not: { anyOf: fields.map(present) } }, ...fields.map(present)],
    }));
  }
}

function present(field) {
  const spellings = [...new Set([field.jsonName, field.name])];
  if (spellings.length === 1) return { required: spellings };
  return { anyOf: spellings.map((spelling) => ({ required: [spelling] })) };
}

const ECMASCRIPT_ESCAPABLE = new Set("^$\\.*+?()[]{}|/-");

export function toECMAScriptPattern(pattern) {
  return pattern.replace(/\\([\s\S])/g, (escaped, char) =>
    /[!-/:-@[-`{-~]/.test(char) && !ECMASCRIPT_ESCAPABLE.has(char) ? char : escaped,
  );
}

function rewriteECMAScriptPatterns(node) {
  if (!node || typeof node !== "object") return;
  if (typeof node.pattern === "string") node.pattern = toECMAScriptPattern(node.pattern);
  for (const value of Object.values(node)) rewriteECMAScriptPatterns(value);
}

function refuseNonECMAScriptPatterns(name, node) {
  if (!node || typeof node !== "object") return;
  if (Array.isArray(node)) {
    for (const item of node) refuseNonECMAScriptPatterns(name, item);
    return;
  }
  if (typeof node.pattern === "string") {
    try {
      new RegExp(node.pattern, "u");
    } catch (error) {
      throw new Error(
        `${name} has a pattern that is not ECMA-262: ${node.pattern} (${error.message})`,
      );
    }
  }
  for (const value of Object.values(node)) refuseNonECMAScriptPatterns(name, value);
}

function writeFile(path, text) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, text);
}

function writeMessageSchemas(dirs, url, messages) {
  for (const dir of dirs) {
    rmSync(dir, { recursive: true, force: true });
    mkdirSync(dir, { recursive: true });
  }
  for (const { name, bundle } of messages) {
    const file = `${name}.schema.json`;
    const text = `${JSON.stringify({ ...bundle, $id: `${url}/${file}` }, null, 2)}\n`;
    for (const dir of dirs) writeFileSync(join(dir, file), text);
  }
}

export function writeSchemas(root, merged, messages) {
  const published = readPublishedSchemas(root);
  const config = published.config.path;
  const configTypes = join(root, "packages", "ocel", "src", "generated", "config.ts");
  const configSelectors = join(root, "pkg", "configdoc", "selectors.json");
  writeFile(config, `${JSON.stringify({ $id: published.config.url, ...merged }, null, 2)}\n`);
  writeFile(configTypes, types(merged));
  writeFile(configSelectors, `${JSON.stringify(selectors(merged), null, 2)}\n`);
  writeMessageSchemas(
    [published.messages.dir, join(root, "cli", "internal", "outputschema", "schemas")],
    published.messages.url,
    messages,
  );
  return [config, configTypes, configSelectors];
}

function build() {
  const formatted = writeSchemas(REPO_ROOT, schema(), generateMessageSchemas());
  execFileSync("pnpm", ["exec", "biome", "format", "--write", ...formatted], {
    cwd: REPO_ROOT,
    stdio: "inherit",
  });
}

if (import.meta.main) build();
