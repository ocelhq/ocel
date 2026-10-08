import app from "ENTRY";
import http from "node:http";
import { getRequest, setResponse } from "@sveltejs/kit/node";

const port = Number(process.env.PORT ?? 3000);
const host = process.env.HOST ?? "0.0.0.0";

http
  .createServer(async (req, res) => {
    const proto =
      String(req.headers["x-forwarded-proto"] ?? "")
        .split(",")[0]
        ?.trim() || "http";
    let request;
    try {
      request = await getRequest({
        base: `${proto}://${req.headers.host ?? "localhost"}`,
        request: req,
      });
    } catch {
      res.statusCode = 400;
      res.end("Bad Request");
      return;
    }
    await setResponse(res, await app.fetch(request));
  })
  .listen(port, host, () => {
    console.log(`Listening on http://${host}:${port}`);
  });
