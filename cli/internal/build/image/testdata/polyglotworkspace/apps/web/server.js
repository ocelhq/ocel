import { greeting } from "@fixture/lib";
import { createServer } from "node:http";

createServer((_, res) => res.end(greeting)).listen(Number(process.env.PORT ?? 8080));
