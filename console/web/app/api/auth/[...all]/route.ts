import { auth, toNextJsHandler } from "@console/auth/next";

export const { GET, POST } = toNextJsHandler(auth);
