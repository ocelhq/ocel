// Minimal ambient types for the Bun-only globals this dev harness uses.
// Scoped here rather than pulling in @types/bun workspace-wide, which
// resolves a second, incompatible drizzle-orm peer variant (built against
// bun-types) alongside the Node one the rest of the workspace uses.
declare const Bun: {
  serve(options: {
    port: number;
    fetch: (request: Request) => Response | Promise<Response>;
  }): { port: number };
};

interface ImportMeta {
  main: boolean;
}
