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
			},
		},
	},
});
