const http = require("http");

const port = Number(process.env.PORT);
const version = process.env.APP_VERSION || "unset";
const boot = Number(process.env.BOOT_DELAY_MS || 0);
const inflight = new Set();

const server = http.createServer((req, res) => {
    if (req.url.startsWith("/slow")) {
        const ms = Number(new URL(req.url, "http://x").searchParams.get("ms") || 2000);
        const timer = setTimeout(() => {
            inflight.delete(timer);
            res.end(version);
        }, ms);
        inflight.add(timer);
        return;
    }
    if (req.url === "/boom") {
        res.statusCode = 500;
        res.end("boom");
        return;
    }
    res.statusCode = Number(process.env.ROOT_STATUS || 200);
    res.end(version);
});

setTimeout(() => {
    server.listen(port, () => console.log(`listening on ${port} as ${version}`));
}, boot);
