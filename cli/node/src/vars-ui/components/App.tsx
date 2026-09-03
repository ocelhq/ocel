import { doneLabel, owedCount, plural } from "../model";
import {
  collapseAll,
  dirty,
  discard,
  expandAll,
  farewell,
  leave,
  outcome,
  save,
  saving,
  state,
} from "../store";
import { Apps } from "./Apps";
import { Sprite } from "./Icons";
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
  const pending = dirty.value.length;
  const busy = saving.value;
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
      </div>
      <footer class="bar">
        <div class="bar-actions">
          <button
            type="button"
            class="primary"
            disabled={busy || pending === 0}
            onClick={() => void save()}
          >
            {busy
              ? "Saving…"
              : pending === 0
                ? "Nothing to save"
                : `Save ${plural(pending, "change")}`}
          </button>
          {pending > 0 && (
            <button type="button" class="linkish" disabled={busy} onClick={discard}>
              discard changes
            </button>
          )}
          <p
            class="outcome"
            aria-live="polite"
            data-tone={outcome.value?.tone}
          >
            {outcome.value?.text}
          </p>
        </div>
        <button type="button" class="done" disabled={busy} onClick={leave}>
          {doneLabel(owedCount(current))}
        </button>
      </footer>
    </div>
  );
}
