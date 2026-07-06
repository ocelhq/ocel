export interface OcelConfig {
  projectId: string;
  discovery?: {
    paths?: string[];
  };
}

export function defineConfig(config: OcelConfig): OcelConfig {
  return config;
}
