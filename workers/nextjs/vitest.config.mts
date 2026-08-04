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
					// The loader is local-only in miniflare, so the edge tests drive
					// the real binding — a mock would prove nothing about whether a
					// bundle actually compiles and runs.
					workerLoaders: { LOADER: {} },
					// A real bucket for the tag-snapshot read tests: the replica is
					// the one thing the edge still reads straight out of R2, and a
					// fake would prove nothing about reading back what a publisher
					// has just written. The write moved to the ISR writer worker,
					// which owns the merge and is tested against its own bucket.
					r2Buckets: ["TAG_SNAPSHOT_STORE"],
				},
			},
		},
	},
});
