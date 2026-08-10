import { auth } from "@repo/auth";

export function authHandler(request: Request): Promise<Response> {
  return auth.handler(request);
}
