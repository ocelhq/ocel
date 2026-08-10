import { handle, type BatchResponse, type SqsRecord } from "./handle.mjs";

export const handler = async (event: { Records?: SqsRecord[] }): Promise<BatchResponse> =>
  handle(
    {
      fetch,
      credentials: {
        accessKeyId: process.env.AWS_ACCESS_KEY_ID ?? "",
        secretAccessKey: process.env.AWS_SECRET_ACCESS_KEY ?? "",
        sessionToken: process.env.AWS_SESSION_TOKEN,
      },
      bucket: process.env.OCEL_ASSET_BUCKET,
      region: process.env.AWS_REGION,
      origins: new Map(),
    },
    event,
  );
