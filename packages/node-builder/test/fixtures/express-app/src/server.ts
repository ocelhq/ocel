import express from "express";
import { greeting } from "./greeting.js";

// Mirrors examples/express: the app calls listen() and never exports itself.
// The generated shim intercepts that listen() to capture the server.
const app = express();
app.use(express.json());

app.get("/hello/:name", (req, res) => {
  res.json({ message: greeting(req.params.name) });
});

const PORT = Number(process.env.PORT ?? 3000);
app.listen(PORT, () => {
  console.log(`fixture express app listening on ${PORT}`);
});
