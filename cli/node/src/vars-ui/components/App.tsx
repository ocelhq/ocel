import { farewell, state } from "../store";
import { Apps } from "./Apps";
import { Inspector } from "./Inspector";
import { Masthead } from "./Masthead";
import { Matrix } from "./Matrix";

export function App() {
  if (farewell.value !== null) {
    return <p class="farewell">{farewell.value}</p>;
  }
  const current = state.value;
  if (!current) {
    return <p class="loading">Reading this project’s variables…</p>;
  }
  return (
    <div class="frame">
      <div class="sheet">
        <Masthead current={current} />
        <p class="eyebrow">
          Apps{" "}
          <span class="axis">— each reads its own folder, then the root</span>
        </p>
        <Apps current={current} />
        <p class="eyebrow">
          Folders{" "}
          <span class="axis">— one column per folder an app can bind</span>
        </p>
        <Matrix current={current} />
      </div>
      <Inspector current={current} />
    </div>
  );
}
