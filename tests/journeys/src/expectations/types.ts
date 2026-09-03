export type ExpectationEnvironment = "aws" | "aws.floci" | "dev" | "vps" | "vps.incus";

export type Expectations = Record<string, Record<string, string>>;
