import { createRouteHandler } from "@ocel/sdk/blob/next";
import { uploads } from "../../../ocel/index";

export const runtime = "nodejs";

// The whole upload surface for the `uploads` bucket: { GET, POST } multiplexing
// ?op=presign|callback|poll. Bytes never flow through here - only presign
// requests, signed completion callbacks, and status polls.
export const { GET, POST } = createRouteHandler(uploads);
