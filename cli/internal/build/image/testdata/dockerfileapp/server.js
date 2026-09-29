const http = require("http");

http
  .createServer((_, res) => {
    res.writeHead(200, { "content-type": "text/plain" });
    res.end(process.env.BUILT_BY || "railpack");
  })
  .listen(Number(process.env.PORT) || 3000);
