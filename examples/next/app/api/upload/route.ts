import { createRouteHandler } from "ocel/blob/next";
import { uploads } from "../../../ocel/index";

export const { GET, POST } = createRouteHandler(uploads);
