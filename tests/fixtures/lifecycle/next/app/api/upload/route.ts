import { createRouteHandler } from "ocel/bucket/next";
import { uploads } from "../../../infra/index";

export const runtime = "nodejs";

export const { GET, POST } = createRouteHandler(uploads);
