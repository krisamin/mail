import { useEffect, useRef, useState, type ReactNode } from "react";

/** Dropdown menu — click to open, outside click or Escape to close. */
export const Menu = ({
  trigger,
  children,
  align = "end",
}: {
  trigger: (props: { open: boolean; toggle: () => void }) => ReactNode;
  children: ReactNode | ((close: () => void) => ReactNode);
  align?: "start" | "end";
}) => {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const close = () => setOpen(false);

  return (
    <div ref={ref} className="relative">
      {trigger({ open, toggle: () => setOpen((v) => !v) })}
      {open && (
        <div
          className={`absolute z-40 mt-1.5 min-w-48 overflow-hidden rounded-lg border border-line bg-surface py-1 shadow-lg shadow-black/20 ${
            align === "end" ? "right-0" : "left-0"
          }`}
          role="menu"
        >
          {typeof children === "function" ? children(close) : children}
        </div>
      )}
    </div>
  );
};

/** Row inside a Menu — renders as a button or wraps a link/form. */
export const MenuItem = ({
  children,
  onClick,
  tone = "normal",
  as = "button",
}: {
  children: ReactNode;
  onClick?: () => void;
  tone?: "normal" | "danger";
  as?: "button" | "div";
}) => {
  const cls = `flex w-full items-center gap-2.5 px-3 py-2 text-left text-sm transition-colors duration-100 ${
    tone === "danger" ? "text-bad hover:bg-bad-soft" : "text-ink-2 hover:bg-raised hover:text-ink"
  }`;
  if (as === "div")
    return (
      <div className={cls} role="menuitem">
        {children}
      </div>
    );
  return (
    <button type="button" role="menuitem" onClick={onClick} className={cls}>
      {children}
    </button>
  );
};

export const MenuDivider = () => <div className="my-1 h-px bg-line" />;
