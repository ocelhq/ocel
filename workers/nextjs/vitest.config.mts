import { defineWorkersConfig } from "@cloudflare/vitest-pool-workers/config";

export default defineWorkersConfig({
	test: {
		poolOptions: {
			workers: {
				wrangler: { configPath: "./wrangler.jsonc" },
				// The cache tests use the real Cache, whose SQLite -shm/-wal files
				// trip the storage stacking this option performs between tests.
				// Those tests key every entry uniquely instead.
				isolatedStorage: false,
			},
		},
	},
});
