import type { ButtonHTMLAttributes, ReactNode } from "react";
import { Link, type LinkProps } from "react-router";
import { useT } from "~/lib/i18n";
import { SpinnerIcon } from "./icon";

// One source of truth for every clickable style in the app.
const variantMap = {
  /** Filled brand action — the single primary action of a view. */
  primary: "bg-brand text-brand-ink hover:bg-brand-hover",
  /** Bordered neutral action. */
  outline: "border border-line-strong text-ink-2 hover:bg-raised hover:text-ink",
  /** Borderless action that only reacts on hover. */
  ghost: "text-ink-2 hover:bg-raised hover:text-ink",
  /** Quiet filled action on a panel. */
  subtle: "bg-raised text-ink-2 hover:bg-line hover:text-ink",
  /** Destructive action. */
  danger: "text-bad hover:bg-bad-soft",
} as const;

const sizeMap = {
  md: "h-9 gap-2 rounded-md px-3.5 text-sm",
  sm: "h-8 gap-1.5 rounded-md px-2.5 text-xs",
  icon: "h-9 w-9 rounded-md",
  iconSm: "h-8 w-8 rounded-md",
} as const;

export type ButtonVariant = keyof typeof variantMap;
export type ButtonSize = keyof typeof sizeMap;

const baseClass =
  "inline-flex shrink-0 items-center justify-center font-medium transition-colors duration-100 disabled:pointer-events-none disabled:opacity-45";

export const buttonClass = (
  variant: ButtonVariant = "primary",
  size: ButtonSize = "md",
  extra = "",
): string => `${baseClass} ${sizeMap[size]} ${variantMap[variant]} ${extra}`;

export const Button = ({
  variant = "primary",
  size = "md",
  className = "",
  pending = false,
  confirmMessage,
  children,
  disabled,
  onClick,
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: ButtonVariant;
  size?: ButtonSize;
  /** Show a spinner + disable while the owning form submits. */
  pending?: boolean;
  /** Guard destructive actions with a confirm dialog. */
  confirmMessage?: string;
}) => {
  const t = useT();
  return (
    <button
      type="submit"
      {...rest}
      disabled={disabled || pending}
      aria-busy={pending || undefined}
      onClick={(e) => {
        if (confirmMessage && !window.confirm(confirmMessage)) {
          e.preventDefault();
          return;
        }
        onClick?.(e);
      }}
      className={buttonClass(variant, size, className)}
    >
      {pending ? (
        <>
          <SpinnerIcon className="size-4 animate-spin" aria-hidden />
          {size !== "icon" && size !== "iconSm" && <span>{t("common.working")}</span>}
        </>
      ) : (
        children
      )}
    </button>
  );
};

export const ButtonLink = ({
  variant = "primary",
  size = "md",
  className = "",
  ...rest
}: LinkProps & { variant?: ButtonVariant; size?: ButtonSize }) => (
  <Link {...rest} className={buttonClass(variant, size, className)} />
);

/** Icon-only button — the label lives in aria-label and the tooltip. */
export const IconButton = ({
  label,
  children,
  size = "icon",
  variant = "ghost",
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & {
  label: string;
  variant?: ButtonVariant;
  size?: Extract<ButtonSize, "icon" | "iconSm">;
  confirmMessage?: string;
  children: ReactNode;
}) => (
  <Button {...rest} variant={variant} size={size} aria-label={label} title={label}>
    {children}
  </Button>
);

/** Active/inactive state pill that submits its form. */
export const ActiveToggle = ({ active, disabled }: { active: boolean; disabled?: boolean }) => {
  const t = useT();
  return (
    <button
      type="submit"
      disabled={disabled}
      aria-pressed={active}
      className={`rounded-md px-2 py-1 text-xs font-medium transition-colors duration-100 disabled:opacity-45 ${
        active ? "bg-ok-soft text-ok hover:brightness-110" : "bg-raised text-ink-3 hover:text-ink"
      }`}
    >
      {active ? t("common.active") : t("common.inactive")}
    </button>
  );
};
