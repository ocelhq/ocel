const http = require("http");

http
  .createServer((_, res) => {
    res.writeHead(200, { "content-type": "text/plain" });
    res.end("plainserver");
  })
  .listen(Number(process.env.PORT) || 3000);
