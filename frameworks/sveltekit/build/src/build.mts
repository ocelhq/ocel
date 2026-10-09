import { type OutputBuild, runAdapterBuild } from "@framework/node-build/script";

export interface SvelteKitBuild extends OutputBuild {
  buildId: string;
}

const ADAPTER = "@ocel/sveltekit";

export async function buildSvelteKit(app: SvelteKitBuild): Promise<void> {
  await runAdapterBuild(
    app,
    {
      framework: "sveltekit",
      name: ADAPTER,
      setup:
        `Add it with \`pnpm add -D ${ADAPTER}\` and name it as the adapter: ` +
        `\`sveltekit({ adapter: ocel() })\` in vite.config on SvelteKit 3, or \`kit: { adapter: ocel() }\` in svelte.config.js on SvelteKit 2, with \`import ocel from "${ADAPTER}"\``,
      upgrade: `Install the ${ADAPTER} release that matches this CLI`,
    },
    { OCEL_BUILD_ID: app.buildId },
  );
  process.stderr.write(`ocel: SvelteKit app "${app.name}" built\n`);
}
