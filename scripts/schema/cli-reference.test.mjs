import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, it } from "node:test";
import { renderIndex, renderPage, writeReference } from "./cli-reference.mjs";

const globalFlags = [
  {
    name: "config",
    shorthand: "c",
    type: "string",
    default: "",
    description: "Project config file",
    persistent: true,
  },
  {
    name: "json",
    shorthand: "",
    type: "bool",
    default: "false",
    description: "Write machine output",
    persistent: true,
  },
];

const envSet = {
  path: "env set",
  short: "Set a value",
  long: "Set a value.\n\nWrites each {KEY} under --folder, with <care>.\n\n  ocel env set A=1 B=2",
  usage: "ocel env set <KEY=VALUE>... [flags]",
  arguments: [{ name: "KEY=VALUE", required: true, variadic: true }],
  aliases: [],
  flags: [
    {
      name: "folder",
      shorthand: "f",
      type: "string",
      default: "/",
      description: "Use the value in this folder",
      persistent: false,
    },
    {
      name: "preview",
      shorthand: "",
      type: "bool",
      default: "false",
      description: "Address previews | not production",
      persistent: false,
    },
    {
      name: "yes",
      shorthand: "",
      type: "bool",
      default: "false",
      description: "Consent",
      persistent: false,
    },
    ...globalFlags,
  ],
  prints_data: true,
  mutates: true,
  confirms: true,
};

const env = {
  path: "env",
  short: "Manage this project's variable values",
  long: "",
  usage: "ocel env <command>",
  arguments: [],
  aliases: [],
  flags: globalFlags,
  prints_data: true,
  mutates: false,
  confirms: false,
};

const envLs = { ...env, path: "env ls", short: "List values", usage: "ocel env ls [flags]" };

describe("renderPage", () => {
  const page = renderPage(envSet, { output: "COMMAND_OUTPUT_RESULT", subcommands: [] });

  it("titles a nested command by its full path and describes it by its short help", () => {
    assert.match(page, /^---\ntitle: "ocel env set"\ndescription: "Set a value\."\n---\n/);
  });

  it("prints the usage line and the long help without repeating the short help", () => {
    assert.ok(page.includes("```bash\nocel env set <KEY=VALUE>... [flags]\n```"));
    assert.ok(!page.includes("Set a value.\n\nWrites"));
    assert.ok(page.includes("Writes each \\{KEY\\} under `--folder`, with &lt;care&gt;."));
    assert.ok(page.includes("```bash\nocel env set A=1 B=2\n```"));
  });

  it("lists the arguments", () => {
    assert.ok(page.includes("| `<KEY=VALUE>...` | Required, and repeatable |"));
  });

  it("keeps an argument's alternatives in one table cell", () => {
    const choice = {
      ...envSet,
      arguments: [{ name: "production|preview", required: true, variadic: false }],
    };
    const rendered = renderPage(choice, { output: "COMMAND_OUTPUT_RESULT", subcommands: [] });
    assert.ok(rendered.includes("| `<production\\|preview>` | Required |"));
  });

  it("tabulates the command's own flags and links the global ones", () => {
    const flags = page.slice(page.indexOf("## Flags"), page.indexOf("## Behaviour"));
    assert.deepEqual(
      flags.split("\n").filter((line) => line.startsWith("| `")),
      [
        "| `-f, --folder` | `string` | `/` | Use the value in this folder |",
        "| `--preview` | `bool` | `false` | Address previews \\| not production |",
        "| `--yes` | `bool` | `false` | Consent |",
      ],
    );
    assert.ok(flags.includes("[global flags](/docs/cli#global-flags)"));
    assert.ok(!flags.includes("--config"));
  });

  it("says that it changes state, asks first, and where the schema of its result envelope is", () => {
    const behaviour = page.slice(page.indexOf("## Behaviour"));
    assert.ok(behaviour.includes("Changes state"));
    assert.ok(behaviour.includes("Asks before it changes anything"));
    assert.ok(behaviour.includes("`--yes`"));
    assert.ok(behaviour.includes("one result envelope"));
    assert.ok(behaviour.includes("`ocel schema env set`"));
  });

  it("says a read-only command changes nothing", () => {
    const ls = renderPage(envLs, { output: "COMMAND_OUTPUT_RESULT", subcommands: [] });
    assert.ok(ls.includes("Read-only"));
    assert.ok(!ls.includes("Changes state"));
  });

  it("puts hand-written prose in place of the long help", () => {
    const withProse = renderPage(envSet, {
      output: "COMMAND_OUTPUT_RESULT",
      subcommands: [],
      prose: "../../../cli/env/set.mdx",
    });
    assert.ok(withProse.includes("<include>../../../cli/env/set.mdx</include>"));
    assert.ok(!withProse.includes("Writes each"));
  });

  it("links a group's subcommands and describes no behaviour of its own", () => {
    const group = renderPage(env, { subcommands: [envSet, envLs] });
    assert.ok(group.includes("| [`ocel env set`](/docs/cli/env/set) | Set a value |"));
    assert.ok(!group.includes("## Behaviour"));
    assert.ok(!group.includes("## Flags"));
  });
});

describe("renderIndex", () => {
  it("lists the top-level commands and the global flags", () => {
    const index = renderIndex([env, envSet, envLs]);
    assert.ok(
      index.includes("| [`ocel env`](/docs/cli/env) | Manage this project's variable values |"),
    );
    assert.ok(!index.includes("ocel env set`]"));
    assert.ok(index.includes("## Global flags"));
    assert.ok(index.includes("| `-c, --config` | `string` |  | Project config file |"));
  });
});

describe("writeReference", () => {
  function scratch() {
    const root = mkdtempSync(join(tmpdir(), "cli-reference-"));
    const docs = join(root, "docs");
    const prose = join(root, "prose");
    mkdirSync(join(docs, "stale"), { recursive: true });
    writeFileSync(join(docs, "stale", "gone.mdx"), "old");
    writeFileSync(
      join(docs, "meta.json"),
      JSON.stringify({ title: "Ocel CLI", pages: ["index", "---Configure---", "gone", "env"] }),
    );
    mkdirSync(join(prose, "env"), { recursive: true });
    writeFileSync(join(prose, "env", "set.mdx"), "Hand-written.\n");
    return { root, docs, prose };
  }

  const catalog = {
    commands: [env, envLs, envSet, { ...envLs, path: "init", short: "Make this deployable" }],
  };
  const outputs = new Map([["env ls", "COMMAND_OUTPUT_RESULT"]]);

  it("nests a group's pages in its folder, includes prose and keeps the navigation's order", () => {
    const { root, docs, prose } = scratch();
    try {
      writeReference({ catalog, outputs, docsDir: docs, proseDir: prose });
      const read = (path) => readFileSync(join(docs, path), "utf8");
      assert.ok(read("env/index.mdx").includes('title: "ocel env"'));
      assert.ok(read("env/set.mdx").includes("<include>../../prose/env/set.mdx</include>"));
      assert.deepEqual(JSON.parse(read("env/meta.json")), {
        title: "ocel env",
        pages: ["index", "ls", "set"],
      });
      assert.deepEqual(JSON.parse(read("meta.json")).pages, [
        "index",
        "---Configure---",
        "env",
        "init",
      ]);
      assert.throws(() => read("stale/gone.mdx"));
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  it("writes the same tree again when nothing changed", () => {
    const { root, docs, prose } = scratch();
    try {
      writeReference({ catalog, outputs, docsDir: docs, proseDir: prose });
      const first =
        readFileSync(join(docs, "meta.json"), "utf8") +
        readFileSync(join(docs, "env/set.mdx"), "utf8");
      writeReference({ catalog, outputs, docsDir: docs, proseDir: prose });
      const second =
        readFileSync(join(docs, "meta.json"), "utf8") +
        readFileSync(join(docs, "env/set.mdx"), "utf8");
      assert.equal(second, first);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  it("refuses prose written for a command that does not exist", () => {
    const { root, docs, prose } = scratch();
    try {
      writeFileSync(join(prose, "deploi.mdx"), "Typo.\n");
      assert.throws(
        () => writeReference({ catalog, outputs, docsDir: docs, proseDir: prose }),
        /deploi\.mdx/,
      );
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });
});
