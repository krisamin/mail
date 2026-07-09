import type { ButtonHTMLAttributes } from "react";
import { Link, type LinkProps } from "react-router";

// Button variants — one source of truth for every clickable style.
const styles = {
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

export type ButtonVariant = keyof typeof styles;

export const Button = ({
  variant = "primary",
  className = "",
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: ButtonVariant }) => (
  <button type="submit" {...props} className={`${styles[variant]} ${className}`} />
);

export const ButtonLink = ({
  variant = "primary",
  className = "",
  ...props
}: LinkProps & { variant?: ButtonVariant }) => (
  <Link {...props} className={`${styles[variant]} ${className}`} />
);

/** Active/inactive toggle — submit button styled as a state pill. */
export const ActiveToggle = ({ active, disabled }: { active: boolean; disabled?: boolean }) => (
  <button
    type="submit"
    disabled={disabled}
    className={`rounded px-2 py-1 text-xs ${
      active ? "bg-ok/20 text-ok hover:bg-ok/30" : "bg-bg-3 text-muted hover:bg-bg-2"
    }`}
  >
    {active ? "활성" : "비활성"}
  </button>
);
