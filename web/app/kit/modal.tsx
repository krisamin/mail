import { useEffect, useRef, type ReactNode } from "react";
import { CloseIcon } from "./icon";
import { IconButton } from "./button";
import { useT } from "~/lib/i18n";

/** Centred dialog. Escape and backdrop close it; focus is trapped by <dialog>. */
export const Modal = ({
  open,
  onClose,
  title,
  children,
  footer,
  width = "md",
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  footer?: ReactNode;
  width?: "sm" | "md" | "lg";
}) => {
  const ref = useRef<HTMLDialogElement>(null);
  const t = useT();

  useEffect(() => {
    const node = ref.current;
    if (!node) return;
    if (open && !node.open) node.showModal();
    if (!open && node.open) node.close();
  }, [open]);

  const widthClass = { sm: "max-w-sm", md: "max-w-lg", lg: "max-w-3xl" }[width];

  return (
    <dialog
      ref={ref}
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      onClick={(e) => {
        // Clicking the backdrop (the dialog element itself) closes.
        if (e.target === ref.current) onClose();
      }}
      className={`m-auto w-[calc(100vw-2rem)] ${widthClass} rounded-lg border border-line bg-surface p-0 text-ink backdrop:bg-black/50`}
    >
      <div className="flex items-center justify-between gap-4 border-b border-line px-4 py-3">
        <h2 className="text-sm font-semibold">{title}</h2>
        <IconButton label={t("common.close")} size="iconSm" onClick={onClose} type="button">
          <CloseIcon className="size-4" />
        </IconButton>
      </div>
      <div className="px-4 py-4">{children}</div>
      {footer && (
        <div className="flex justify-end gap-2 border-t border-line px-4 py-3">{footer}</div>
      )}
    </dialog>
  );
};
