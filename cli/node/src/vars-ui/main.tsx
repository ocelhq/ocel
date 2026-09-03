import "./styles.css";

import { render } from "preact";

import { App } from "./components/App";
import { load } from "./store";

const root = document.getElementById("root")!;
render(<App />, root);
void load().finally(() => root.setAttribute("aria-busy", "false"));
