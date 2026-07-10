import type { ButtonHTMLAttributes } from "react";
import { Link, type LinkProps } from "react-router";
import { useT } from "~/lib/i18n";

// Button variants — one source of truth for every clickable style.
const styleMap = {
  /** Solid accent CTA (form submit, primary action) */
  primary:
    "rounded-md bg-accent px-4 py-2 text-center text-sm font-medium text-bg-0 hover:bg-accent-hover disabled:opacity-50",
  /** Bordered neutral action */
  outline:
    "rounded-md border border-line px-4 py-2 text-center text-sm text-text-1 hover:bg-bg-2 disabled:opacity-50",
  /** Small pill action (secondary, inline) */
  chip: "rounded bg-bg-3 px-2 py-1 text-xs text-text-2 hover:bg-bg-2 disabled:opacity-50",
  /** Inline text action */
  link: "text-xs text-accent hover:underline disabled:opacity-50",
  /** Inline destructive text action */
  linkDanger: "text-xs text-bad hover:underline disabled:opacity-50",
} as const;

export type ButtonVariant = keyof typeof styleMap;

export const Button = ({
  variant = "primary",
  className = "",
  pending = false,
  confirmMessage,
  children,
  disabled,
  onClick,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: ButtonVariant;
  /** Show a working label + disable while the owning form is submitting. */
  pending?: boolean;
  /** Destructive-action guard — window.confirm before the submit goes out. */
  confirmMessage?: string;
}) => {
  const t = useT();
  return (
    <button
      type="submit"
      {...props}
      disabled={disabled || pending}
      aria-busy={pending || undefined}
      onClick={(e) => {
        if (confirmMessage && !window.confirm(confirmMessage)) {
          e.preventDefault();
          return;
        }
        onClick?.(e);
      }}
      className={`${styleMap[variant]} ${className}`}
    >
      {pending ? t("common.working") : children}
    </button>
  );
};

export const ButtonLink = ({
  variant = "primary",
  className = "",
  ...props
}: LinkProps & { variant?: ButtonVariant }) => (
  <Link {...props} className={`${styleMap[variant]} ${className}`} />
);

/** Active/inactive toggle — submit button styled as a state pill. */
export const ActiveToggle = ({ active, disabled }: { active: boolean; disabled?: boolean }) => {
  const t = useT();
  return (
    <button
      type="submit"
      disabled={disabled}
      aria-pressed={active}
      className={`rounded px-2 py-1 text-xs transition-colors duration-100 ${
        active ? "bg-ok/20 text-ok hover:bg-ok/30" : "bg-bg-3 text-muted hover:bg-bg-2"
      }`}
    >
      {active ? t("common.active") : t("common.inactive")}
    </button>
  );
};
