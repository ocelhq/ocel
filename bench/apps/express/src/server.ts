import { MARKER, app } from "./app.js";

const PORT = Number(process.env.PORT ?? 3301);

app.listen(PORT, () => {
  console.log(`${MARKER} listening on http://localhost:${PORT}`);
});
