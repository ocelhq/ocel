import { auth, toNextJsHandler } from "@repo/auth/next";

export const { GET, POST } = toNextJsHandler(auth);
