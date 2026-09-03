import { collapseAll, error, expandAll, farewell, state } from "../store";
import { Apps } from "./Apps";
import { Sprite } from "./Icons";
import { Inspector } from "./Inspector";
import { Masthead } from "./Masthead";
import { Table } from "./Table";

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
      <Sprite />
      <div class="sheet">
        <Masthead current={current} />
        <p class="eyebrow">
          Apps{" "}
          <span class="axis">— each reads its own folder, then the root</span>
        </p>
        <Apps current={current} />
        <div class="toolbar">
          <p class="eyebrow">
            Variables{" "}
            <span class="axis">
              — one row per key; folder and environment variants nest under it
            </span>
          </p>
          <div class="tools">
            <button type="button" class="linkish" onClick={expandAll}>
              expand all
            </button>
            <button type="button" class="linkish" onClick={collapseAll}>
              collapse all
            </button>
          </div>
        </div>
        <Table />
        <p class="outcome" aria-live="polite" data-tone={error.value ? "owed" : undefined}>
          {error.value}
        </p>
      </div>
      <Inspector current={current} />
    </div>
  );
}
