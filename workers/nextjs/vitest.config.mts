import { defineWorkersConfig } from "@cloudflare/vitest-pool-workers/config";

export default defineWorkersConfig({
	test: {
		poolOptions: {
			workers: {
				wrangler: { configPath: "./wrangler.jsonc" },
				// The Cache API is not covered by isolated storage; snapshotting the
				// CacheObject fails non-deterministically. Cache tests key each case
				// uniquely instead.
				isolatedStorage: false,
				miniflare: {
					// wrangler.jsonc's DEPLOYMENTS binding names the real
					// deployments-store worker, which isn't running under test — every
					// test drives resolveDeployment/dispatchResult directly with a
					// synthetic binding, never through this env binding, so it only
					// needs to resolve, never to answer correctly.
					serviceBindings: {
						DEPLOYMENTS: () => new Response(null, { status: 501 }),
					},
				},
			},
		},
	},
});
