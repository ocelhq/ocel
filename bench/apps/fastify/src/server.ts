import { MARKER, app } from "./app.js";

const PORT = Number(process.env.PORT ?? 3303);

app.listen({ port: PORT, host: "0.0.0.0" }, () => {
  console.log(`${MARKER} listening on http://localhost:${PORT}`);
});
