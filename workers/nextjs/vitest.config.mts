import { defineWorkersConfig } from "@cloudflare/vitest-pool-workers/config";

export default defineWorkersConfig({
	test: {
		poolOptions: {
			workers: {
				wrangler: { configPath: "./wrangler.jsonc" },
				isolatedStorage: false,
				miniflare: {
					serviceBindings: {
						DEPLOYMENTS: () => new Response(null, { status: 501 }),
					},
					workerLoaders: { LOADER: {} },
					r2Buckets: ["TAG_SNAPSHOT_STORE"],
				},
			},
		},
	},
});
