import { folderName, type State } from "../model";
import { hoveredApp } from "../store";

export function Apps({ current }: { current: State }) {
  return (
    <ul class="apps">
      {current.matrix.apps.map((app) => {
        const missing = app.missing ?? [];
        return (
          <li
            class="app"
            key={app.name}
            onMouseEnter={() => (hoveredApp.value = app)}
            onMouseLeave={() => (hoveredApp.value = null)}
          >
            <span class="name">{app.name}</span>
            <span class="binding">reads {folderName(app.folder)}, then root</span>
            <span class="verdict" data-resolves={missing.length === 0}>
              {missing.length === 0 ? "resolves" : `${missing.length} unresolved`}
            </span>
          </li>
        );
      })}
    </ul>
  );
}
