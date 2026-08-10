import { auth } from "@console/auth";

export function authHandler(request: Request): Promise<Response> {
  return auth.handler(request);
}
