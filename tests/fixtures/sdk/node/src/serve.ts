import type { NextFunction, Request, Response } from "express";

export type JSONRoute = (req: Request, res: Response) => Promise<unknown>;

export function serveJSON(serve: JSONRoute) {
  return (req: Request, res: Response, next: NextFunction) => {
    serve(req, res).then((body) => res.json(body), next);
  };
}
