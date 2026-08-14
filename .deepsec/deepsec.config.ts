import { defineConfig } from "deepsec/config";
import { generatedMatchersPlugin } from "./generated-matchers.js";

export default defineConfig({
  defaultThinkingLevel: "high", // <deepsec:default-thinking-level>
  defaultModel: "gpt-5.6-sol", // <deepsec:default-model>
  defaultAgent: "codex", // <deepsec:default-agent>
  ai: {"mode":"local","provider":"local"}, // <deepsec:model-route>
  projects: [
    { id: "ocelhq", root: ".." },
    // <deepsec:projects-insert-above>
  ],
  plugins: [generatedMatchersPlugin],
});
