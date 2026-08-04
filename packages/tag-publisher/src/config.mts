import { GetParameterCommand, SSMClient } from "@aws-sdk/client-ssm";

// What the function needs before it can publish anything: the bucket its copy
// of every build's clock lives in, the writer's endpoint, and the substrate's
// write-secret seed. Bootstrap names all three in the environment
// (cloud/aws/bootstrap/publisher.go); a function missing any of them cannot
// degrade into doing half the job, so it refuses to start.
export interface Config {
  assetBucket: string;
  endpoint: string;
  seed: string;
}

function required(name: string): string {
  const value = process.env[name];
  if (value === undefined || value === "") {
    throw new Error(`${name} is not set; this function cannot publish without it`);
  }
  return value;
}

async function parameter(ssm: SSMClient, name: string): Promise<string> {
  const out = await ssm.send(
    new GetParameterCommand({ Name: name, WithDecryption: true }),
  );
  const value = out.Parameter?.Value;
  if (value === undefined || value === "") {
    throw new Error(`SSM parameter ${name} is empty`);
  }
  return value;
}

async function read(ssm: SSMClient): Promise<Config> {
  const [writer, seed] = await Promise.all([
    parameter(ssm, required("OCEL_ISR_WRITER_PARAM")),
    parameter(ssm, required("OCEL_ISR_WRITER_SEED_PARAM")),
  ]);
  const { endpoint } = JSON.parse(writer) as { endpoint?: string };
  if (endpoint === undefined || endpoint === "") {
    throw new Error("the adopted ISR writer has no endpoint; re-run `ocel bootstrap`");
  }
  return { assetBucket: required("OCEL_ASSET_BUCKET"), endpoint, seed };
}

// Read once per container and shared by every invocation it serves. Held as the
// promise rather than the value so a burst on a cold start makes one call
// rather than one apiece, and dropped on failure so the next invocation retries
// instead of inheriting the rejection forever.
let pending: Promise<Config> | undefined;

export function config(ssm: SSMClient): Promise<Config> {
  return (pending ??= read(ssm).catch((err) => {
    pending = undefined;
    throw err;
  }));
}
