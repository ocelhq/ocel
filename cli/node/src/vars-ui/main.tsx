import { install, store } from "@ui/vars";
import { createRoot } from "react-dom/client";

import { loopback, session } from "./api";
import { App } from "./components/App";

install(loopback, session);

const root = document.getElementById("root")!;
createRoot(root).render(<App />);
void store.load().finally(() => root.setAttribute("aria-busy", "false"));

window.addEventListener("beforeunload", (event) => {
  if (store.dirty.value.length === 0) return;
  event.preventDefault();
});
