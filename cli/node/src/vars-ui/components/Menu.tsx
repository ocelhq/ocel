import { useEffect, useRef, useState } from "preact/hooks";

import { Icon, type IconName } from "./Icons";

export interface MenuItem {
  label: string;
  icon?: IconName;
  danger?: boolean;
  disabled?: boolean;
  onSelect: () => void;
}

export interface MenuSection {
  label?: string;
  items: MenuItem[];
}

export function Menu({
  label,
  sections,
}: {
  label: string;
  sections: MenuSection[];
}) {
  const [open, setOpen] = useState(false);
  const [place, setPlace] = useState({ top: 0, right: 0 });
  const trigger = useRef<HTMLButtonElement>(null);
  const list = useRef<HTMLDivElement>(null);

  const close = (refocus: boolean) => {
    setOpen(false);
    if (refocus) trigger.current?.focus();
  };

  useEffect(() => {
    if (!open) return;
    const items = () =>
      [...(list.current?.querySelectorAll<HTMLElement>("[role='menuitem']:not(:disabled)") ?? [])];
    items()[0]?.focus({ preventScroll: true });
    const onKey = (event: KeyboardEvent) => {
      const all = items();
      const index = all.indexOf(document.activeElement as HTMLElement);
      if (event.key === "Escape") {
        event.preventDefault();
        close(true);
      } else if (event.key === "ArrowDown") {
        event.preventDefault();
        all[(index + 1) % all.length]?.focus();
      } else if (event.key === "ArrowUp") {
        event.preventDefault();
        all[(index - 1 + all.length) % all.length]?.focus();
      } else if (event.key === "Tab") {
        close(false);
      }
    };
    const onPointer = (event: PointerEvent) => {
      const target = event.target;
      if (!(target instanceof Node)) return;
      if (list.current?.contains(target) || trigger.current?.contains(target)) return;
      close(false);
    };
    const onAway = () => close(false);
    window.addEventListener("keydown", onKey);
    window.addEventListener("pointerdown", onPointer);
    window.addEventListener("scroll", onAway, true);
    window.addEventListener("resize", onAway);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("pointerdown", onPointer);
      window.removeEventListener("scroll", onAway, true);
      window.removeEventListener("resize", onAway);
    };
  }, [open]);

  const toggle = () => {
    if (open) {
      close(false);
      return;
    }
    const box = trigger.current?.getBoundingClientRect();
    if (box) {
      setPlace({ top: box.bottom + 4, right: window.innerWidth - box.right });
    }
    setOpen(true);
  };

  const shown = sections.filter((section) => section.items.length > 0);

  return (
    <>
      <button
        type="button"
        class="iconbtn"
        ref={trigger}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={label}
        title="More"
        onClick={toggle}
      >
        <Icon name="more" />
      </button>
      {open && (
        <div
          class="menu"
          role="menu"
          aria-label={label}
          ref={list}
          style={{ top: `${place.top}px`, right: `${place.right}px` }}
        >
          {shown.map((section, index) => (
            <div class="menu-section" key={index}>
              {section.label && <p class="menu-label">{section.label}</p>}
              {section.items.map((item) => (
                <button
                  type="button"
                  role="menuitem"
                  class="menu-item"
                  data-danger={item.danger}
                  disabled={item.disabled}
                  key={item.label}
                  onClick={() => {
                    close(true);
                    item.onSelect();
                  }}
                >
                  {item.icon && <Icon name={item.icon} />}
                  {item.label}
                </button>
              ))}
            </div>
          ))}
        </div>
      )}
    </>
  );
}
