import express from "express";
import fs from "fs";
import path from "path";

const app = express();

app.get("/", (req, res) => {
  const targetPath = req.query.path?.toString() || "/var/task";

  try {
    const stats = fs.statSync(targetPath);

    if (stats.isDirectory()) {
      const files = fs.readdirSync(targetPath);
      return res.json({
        type: "directory",
        path: targetPath,
        files,
        env: process.env,
      });
    }

    // If it's a file, read it
    const content = fs.readFileSync(targetPath, "utf-8");

    res.type("text/plain");
    res.send(content);
  } catch (error: any) {
    return res.status(500).json({ error: error.message });
  }
});

app.get("/api/users/:id", (_req, res) => {
  res.json({ id: _req.params.id });
});

app.get("/api/posts/:postId/comments/:commentId", (_req, res) => {
  res.json({ postId: _req.params.postId, commentId: _req.params.commentId });
});

app.listen(3000, () => {
  console.log("Server is running...");
});
