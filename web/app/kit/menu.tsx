import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";

// Dropdown menu — click to open, outside click or Escape to close.
//
// The panel is positioned in fixed coordinates rather than hung under the
// trigger: the rail sits at the very bottom of the window, and a menu that
// always drops downwards there falls off the screen (and the document no
// longer scrolls, so it is simply lost). It measures instead, and flips to
// whichever side has room.

type Side = "bottom" | "top" | "right";

type Position = { top: number; left: number };

const GAP = 6;
const MARGIN = 8;

export const Menu = ({
  trigger,
  children,
  align = "end",
  side = "bottom",
}: {
  trigger: (props: { open: boolean; toggle: () => void }) => ReactNode;
  children: ReactNode | ((close: () => void) => ReactNode);
  align?: "start" | "end";
  /** Preferred side; it flips when the panel would not fit. */
  side?: Side;
}) => {
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState<Position | null>(null);
  const anchorRef = useRef<HTMLDivElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      const target = e.target as Node;
      if (anchorRef.current?.contains(target) || panelRef.current?.contains(target)) return;
      setOpen(false);
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

  // Measure after the panel exists but before the browser paints, so it never
  // shows up in the wrong place first.
  useLayoutEffect(() => {
    if (!open) {
      setPosition(null);
      return;
    }
    const place = () => {
      const anchor = anchorRef.current?.getBoundingClientRect();
      const panel = panelRef.current?.getBoundingClientRect();
      if (!anchor || !panel) return;
      const vw = window.innerWidth;
      const vh = window.innerHeight;

      let top: number;
      let left: number;

      if (side === "right") {
        left = anchor.right + GAP;
        // bottom-aligned: menus next to a rail read better rising from the icon
        top = anchor.bottom - panel.height;
        if (left + panel.width > vw - MARGIN) left = anchor.left - GAP - panel.width;
      } else {
        const below = vh - anchor.bottom;
        const wantTop = side === "top" || (below < panel.height + GAP + MARGIN && anchor.top > below);
        top = wantTop ? anchor.top - GAP - panel.height : anchor.bottom + GAP;
        left = align === "end" ? anchor.right - panel.width : anchor.left;
      }

      // keep the whole panel on screen whatever the side decided
      left = Math.min(Math.max(MARGIN, left), Math.max(MARGIN, vw - panel.width - MARGIN));
      top = Math.min(Math.max(MARGIN, top), Math.max(MARGIN, vh - panel.height - MARGIN));
      setPosition({ top, left });
    };
    place();
    window.addEventListener("resize", place);
    window.addEventListener("scroll", place, true);
    return () => {
      window.removeEventListener("resize", place);
      window.removeEventListener("scroll", place, true);
    };
  }, [open, side, align]);

  const close = () => setOpen(false);

  return (
    <div ref={anchorRef} className="relative">
      {trigger({ open, toggle: () => setOpen((v) => !v) })}
      {open && (
        <div
          ref={panelRef}
          className="fixed z-50 min-w-48 overflow-hidden rounded-lg border border-line bg-surface py-1 shadow-lg shadow-black/20"
          style={{
            top: position?.top ?? -9999,
            left: position?.left ?? -9999,
            // hidden until measured — one frame, but a visible jump otherwise
            visibility: position ? "visible" : "hidden",
          }}
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
    <button type="button" className={cls} role="menuitem" onClick={onClick}>
      {children}
    </button>
  );
};

export const MenuDivider = () => <div className="my-1 h-px bg-line" />;
