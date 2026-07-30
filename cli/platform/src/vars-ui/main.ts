// The variables UI. It renders the required-cell matrix the CLI derived — it
// never derives one of its own, because the rules that decide a cell belong to
// the declaration, and a second copy of them here could only ever disagree.
import "./styles.css";

type CellState = "required" | "optional" | "forbidden";

interface Cell {
  key: string;
  folder: string;
}

interface MatrixCell {
  folder: string;
  state: CellState;
  set: boolean;
  version: number;
  overrides?: string[];
  problem?: string;
}

interface MatrixRow {
  key: string;
  class: string;
  scope?: string[];
  cells: MatrixCell[];
}

interface AppResolution {
  name: string;
  folder: string;
  missing?: Cell[];
}

interface State {
  slug: string;
  substrate: string;
  matrix: {
    columns: string[];
    rows: MatrixRow[];
    apps: AppResolution[];
  };
}

interface Version {
  version: number;
  createdAt: number;
  size: number;
}

// The token arrives in the fragment, which no browser sends to any server. It
// is read once and erased from the address bar so a screen share or a pasted
// URL cannot carry it.
const token = new URLSearchParams(location.hash.slice(1)).get("t") ?? "";
history.replaceState(null, "", location.pathname);

const root = document.getElementById("root")!;

let state: State | null = null;
let selected: Cell | null = null;
let history_: Version[] = [];
let draft = "";
let error = "";
let thread: AppResolution | null = null;
let saving = false;

// ApiError carries the status because one of them is not a failure: a refused
// write means this page was showing a value that has since changed, and the
// answer to that is to show what is there now.
class ApiError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  const response = await fetch(path, {
    method,
    headers: {
      Authorization: `Bearer ${token}`,
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await response.text();
  if (!response.ok) {
    let message = text;
    try {
      message = JSON.parse(text).error ?? text;
    } catch {
      /* a guard rejection answers in plain text */
    }
    throw new ApiError(response.status, message.trim());
  }
  return text ? (JSON.parse(text) as T) : (undefined as T);
}

function query(cell: Cell): string {
  return `key=${encodeURIComponent(cell.key)}&folder=${encodeURIComponent(cell.folder)}`;
}

function cellOf(row: MatrixRow, folder: string): MatrixCell | undefined {
  return row.cells.find((cell) => cell.folder === folder);
}

function owedCount(current: State): number {
  return current.matrix.rows.reduce(
    (total, row) =>
      total +
      row.cells.filter(
        (cell) => (cell.state === "required" && !cell.set) || cell.problem,
      ).length,
    0,
  );
}

function socketState(cell: MatrixCell): string {
  if (cell.state === "forbidden") return "forbidden";
  if (cell.problem) return "faulty";
  if (cell.set) return "held";
  return cell.state === "required" ? "owed" : "free";
}

function folderName(folder: string): string {
  return folder === "" ? "root" : folder;
}

// A project can hold a preview environment per open pull request, and naming
// fifty of them is a wall of text where a sentence was meant. Past this many
// the rest are counted rather than listed.
const namedLimit = 5;

function names(items: string[]): string {
  const shown = items.slice(0, namedLimit);
  const rest = items.length - shown.length;
  if (rest > 0) shown.push(`${rest} other${rest === 1 ? "" : "s"}`);
  if (shown.length <= 2) return shown.join(" and ");
  return `${shown.slice(0, -1).join(", ")} and ${shown[shown.length - 1]}`;
}

// What this cell reaches. The class-wide value is the one every environment
// reads, but only where none has been given its own — saying so unconditionally
// would describe a cell that is not the one on screen.
function coordinateLine(row: MatrixRow, cell: MatrixCell): string {
  const where = `${folderName(cell.folder)} · ${row.class}`;
  const overrides = cell.overrides ?? [];
  if (overrides.length === 0) {
    return `${where} · class-wide, every environment reads it`;
  }
  return `${where} · class-wide, except in ${names(overrides)}`;
}

function element<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  className?: string,
  text?: string,
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

async function load(): Promise<void> {
  state = await api<State>("GET", "/api/state");
  render();
}

async function refreshHistory(): Promise<void> {
  history_ = [];
  if (!selected) return;
  const cell = selected;
  const { versions } = await api<{ versions: Version[] }>(
    "GET",
    `/api/history?${query(cell)}`,
  );
  if (selected && selected.key === cell.key && selected.folder === cell.folder) {
    history_ = versions;
    render();
  }
}

function select(row: MatrixRow, cell: MatrixCell): void {
  if (cell.state === "forbidden") return;
  selected = { key: row.key, folder: cell.folder };
  draft = "";
  error = "";
  history_ = [];
  render();
  void refreshHistory().catch(() => {
    /* history is context, not the task; its absence must not block a fix */
  });
}

async function mutate(run: () => Promise<State>): Promise<void> {
  saving = true;
  error = "";
  render();
  try {
    state = await run();
    draft = "";
    await refreshHistory();
  } catch (thrown) {
    error = thrown instanceof Error ? thrown.message : String(thrown);
    if (thrown instanceof ApiError && thrown.status === 409) {
      // A refusal promises the page is showing what is there now, and the
      // other writer's version is the evidence that explains it. A re-read
      // that fails leaves neither, so the promise is withdrawn rather than
      // left standing over stale content.
      try {
        state = await api<State>("GET", "/api/state");
        await refreshHistory();
      } catch {
        error = `${error} The page could not re-read this cell either, so what is on screen may still be out of date.`;
      }
    }
  } finally {
    saving = false;
    render();
  }
}

function renderMasthead(current: State): HTMLElement {
  const masthead = element("header", "masthead");
  const slug = element("h1", "slug");
  slug.append(current.slug, " ");
  slug.append(element("span", "substrate", `· ${current.substrate}`));
  masthead.append(slug);

  const owed = owedCount(current);
  const tally = element(
    "p",
    "tally",
    owed === 0
      ? "every required cell is filled"
      : `${owed} cell${owed === 1 ? "" : "s"} to fill`,
  );
  tally.dataset.owed = String(owed);
  masthead.append(tally);
  return masthead;
}

function renderApps(current: State): HTMLElement {
  const list = element("ul", "apps");
  for (const app of current.matrix.apps) {
    const missing = app.missing ?? [];
    const item = element("li", "app");
    item.append(element("span", "name", app.name));
    item.append(
      element("span", "binding", `reads ${folderName(app.folder)}, then root`),
    );

    const verdict = element(
      "span",
      "verdict",
      missing.length === 0 ? "resolves" : `${missing.length} unresolved`,
    );
    verdict.dataset.resolves = String(missing.length === 0);
    item.append(verdict);

    item.addEventListener("mouseenter", () => {
      thread = app;
      render();
    });
    item.addEventListener("mouseleave", () => {
      thread = null;
      render();
    });
    list.append(item);
  }
  return list;
}

function renderMatrix(current: State): HTMLElement {
  const scroller = element("div", "scroller");
  const table = element("table", "matrix");
  if (thread) table.dataset.threading = "true";

  const head = element("thead");
  const headRow = element("tr");
  headRow.append(element("th", "corner", "variable"));
  for (const column of current.matrix.columns) {
    headRow.append(element("th", "column", folderName(column)));
  }
  head.append(headRow);
  table.append(head);

  const body = element("tbody");
  for (const row of current.matrix.rows) {
    const tr = element("tr");
    const header = element("th");
    header.append(element("span", "key", row.key));
    header.append(
      element(
        "span",
        "class",
        row.scope?.length
          ? `${row.class} · scoped to ${row.scope.join(" and ")}`
          : row.class,
      ),
    );
    tr.append(header);

    for (const column of current.matrix.columns) {
      const td = element("td");
      const cell = cellOf(row, column);
      if (cell) td.append(renderSocket(row, cell));
      tr.append(td);
    }
    body.append(tr);
  }
  table.append(body);
  scroller.append(table);
  return scroller;
}

function renderSocket(row: MatrixRow, cell: MatrixCell): HTMLElement {
  const where = folderName(cell.folder);
  const socket =
    cell.state === "forbidden"
      ? forbiddenSocket(row, where)
      : liveSocket(row, cell, where);
  socket.dataset.state = socketState(cell);
  socket.append(element("span", "pip"));

  // The thread renders the two hops literally: an app reads its own folder,
  // then the root, and nothing else.
  if (thread && (cell.folder === "" || cell.folder === thread.folder)) {
    socket.classList.add("threaded");
  }
  return socket;
}

// A forbidden cell is not a disabled control — it is not a control. There is
// nothing to press because nothing could ever read a value from it.
function forbiddenSocket(row: MatrixRow, where: string): HTMLElement {
  const socket = element("span", "socket forbidden");
  socket.title = `${row.key} holds no value in ${where} — nothing would read one`;
  socket.setAttribute("role", "img");
  socket.setAttribute("aria-label", socket.title);
  return socket;
}

function liveSocket(
  row: MatrixRow,
  cell: MatrixCell,
  where: string,
): HTMLElement {
  const socket = element("button", "socket");
  socket.type = "button";
  socket.title = `${row.key} in ${where}: ${cell.state}${cell.set ? ", set" : ", not set"}`;
  socket.setAttribute("aria-label", socket.title);
  socket.setAttribute(
    "aria-pressed",
    String(selected?.key === row.key && selected.folder === cell.folder),
  );
  socket.addEventListener("click", () => select(row, cell));
  return socket;
}

function renderInspector(current: State): HTMLElement {
  const inspector = element("aside", "inspector");

  if (!selected) {
    inspector.append(element("h2", undefined, "Pick a cell"));
    inspector.append(
      element(
        "p",
        "empty",
        "Each socket is one variable in one folder. Hollow sockets are owed; hatched sockets hold no value because nothing would read one.",
      ),
    );
    inspector.append(renderLegend());
    inspector.append(renderDone(current));
    return inspector;
  }

  const row = current.matrix.rows.find((candidate) => candidate.key === selected!.key);
  const cell = row && cellOf(row, selected.folder);
  if (!row || !cell) {
    selected = null;
    return renderInspector(current);
  }

  inspector.append(element("h2", undefined, row.key));
  inspector.append(element("p", "coordinate", coordinateLine(row, cell)));

  const verdict = element(
    "p",
    "verdict-line",
    cell.set
      ? "A value is set here."
      : cell.state === "required"
        ? "No value is set, and this cell is required."
        : "No value is set. This cell overrides the root when you set one.",
  );
  if (!cell.set && cell.state === "required") verdict.dataset.tone = "owed";
  inspector.append(verdict);

  if (cell.problem) {
    inspector.append(
      element("p", "problem", `The value here fails its schema: ${cell.problem}`),
    );
  }

  const field = element("div", "field");
  const label = element("label", undefined, cell.set ? "New value" : "Value");
  label.htmlFor = "value";
  const input = element("input");
  input.id = "value";
  input.type = "text";
  input.value = draft;
  input.autocomplete = "off";
  input.spellcheck = false;
  input.placeholder = cell.set ? "replace the value that is set" : "";
  input.addEventListener("input", () => {
    draft = input.value;
    save.disabled = saving || draft === "";
  });
  input.addEventListener("keydown", (event) => {
    if (event.key === "Enter" && draft !== "") void write();
  });
  field.append(label, input);
  inspector.append(field);

  const actions = element("div", "actions");
  const save = element("button", "save", saving ? "Saving…" : "Save");
  save.type = "button";
  save.disabled = saving || draft === "";
  save.addEventListener("click", () => void write());
  actions.append(save);

  if (cell.set) {
    const remove = element("button", "remove", "Remove");
    remove.type = "button";
    remove.disabled = saving;
    remove.addEventListener("click", () => void erase());
    actions.append(remove);
  }
  inspector.append(actions);

  const overrides = cell.overrides ?? [];
  if (overrides.length > 0) {
    inspector.append(
      element(
        "p",
        "empty",
        `${names(overrides)} ${overrides.length === 1 ? "holds its own value" : "hold their own values"} for ${row.key}. Save and Remove here address the class-wide value only; reach ${overrides.length === 1 ? "that one" : "those"} with ocel env set --environment.`,
      ),
    );
  }

  if (error) inspector.append(element("p", "problem", error));

  inspector.append(element("p", "eyebrow", "History"));
  if (history_.length === 0) {
    inspector.append(element("p", "empty", "No versions yet."));
  } else {
    const list = element("ul", "history");
    for (const version of history_) {
      const item = element("li");
      item.append(element("span", undefined, `v${version.version}`));
      item.append(
        element(
          "span",
          undefined,
          `${new Date(version.createdAt * 1000).toLocaleString()} · ${version.size} bytes`,
        ),
      );
      list.append(item);
    }
    inspector.append(list);
  }

  inspector.append(renderDone(current));
  return inspector;

  async function write(): Promise<void> {
    const cellNow = selected!;
    const value = draft;
    const version = cell!.version;
    await mutate(() =>
      api<State>("PUT", "/api/value", { ...cellNow, value, version }),
    );
  }

  async function erase(): Promise<void> {
    const cellNow = selected!;
    const version = cell!.version;
    await mutate(() =>
      api<State>("DELETE", `/api/value?${query(cellNow)}&version=${version}`),
    );
  }
}

function renderLegend(): HTMLElement {
  const legend = element("div", "legend");
  for (const [state, caption] of [
    ["held", "set"],
    ["owed", "required, empty"],
    ["free", "optional override"],
    ["forbidden", "nothing would read it"],
  ] as const) {
    const entry = element("span");
    const swatch = element("span", "socket");
    swatch.dataset.state = state;
    if (state === "forbidden") swatch.classList.add("forbidden");
    swatch.append(element("span", "pip"));
    entry.append(swatch, caption);
    legend.append(entry);
  }
  return legend;
}

function renderDone(current: State): HTMLElement {
  const owed = owedCount(current);
  const done = element(
    "button",
    "done",
    owed === 0
      ? "Done — return to the terminal"
      : `Return to the terminal with ${owed} cell${owed === 1 ? "" : "s"} still to fill`,
  );
  done.type = "button";
  done.addEventListener("click", () => {
    void api("POST", "/api/done").catch(() => {
      /* the session closes as it answers, so a dropped response is success */
    });
    root.replaceChildren(
      element("p", "farewell", "Returned to the terminal. You can close this tab."),
    );
  });
  return done;
}

function render(): void {
  if (!state) return;
  const frame = element("div", "frame");
  const sheet = element("div", "sheet");

  sheet.append(renderMasthead(state));
  const appsHeading = element("p", "eyebrow", "Apps ");
  appsHeading.append(
    element("span", "axis", "— each reads its own folder, then the root"),
  );
  sheet.append(appsHeading, renderApps(state));

  const matrixHeading = element("p", "eyebrow", "Folders ");
  matrixHeading.append(
    element("span", "axis", "— one column per folder an app can bind"),
  );
  sheet.append(matrixHeading, renderMatrix(state));

  frame.append(sheet, renderInspector(state));
  root.replaceChildren(frame);
  root.setAttribute("aria-busy", "false");
}

load().catch((thrown: unknown) => {
  root.replaceChildren(
    element(
      "p",
      "farewell",
      `Could not read this project's variables: ${thrown instanceof Error ? thrown.message : String(thrown)}`,
    ),
  );
});
