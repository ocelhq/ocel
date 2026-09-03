import { createRoot } from "react-dom/client";

import { App } from "./components/App";
import { dirty, load } from "./store";

const root = document.getElementById("root")!;
createRoot(root).render(<App />);
void load().finally(() => root.setAttribute("aria-busy", "false"));

window.addEventListener("beforeunload", (event) => {
  if (dirty.value.length === 0) return;
  event.preventDefault();
});
