import http from "node:http";

http.createServer((_, res) => res.end("ok")).listen(8080);
