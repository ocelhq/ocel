import "./styles.css";

import { render } from "preact";

import { App } from "./components/App";
import { dirty, load } from "./store";

const root = document.getElementById("root")!;
render(<App />, root);
void load().finally(() => root.setAttribute("aria-busy", "false"));

window.addEventListener("beforeunload", (event) => {
  if (dirty.value.length === 0) return;
  event.preventDefault();
  event.returnValue = "";
});
