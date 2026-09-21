import type { ReactNode } from "react";

// A switch, not a checkbox: these turn a capability on and off, and the
// difference should read at a glance in a list of them.

export const Switch = ({
  checked,
  onChange,
  label,
  hint,
  disabled,
  name,
}: {
  checked: boolean;
  onChange?: (next: boolean) => void;
  label: ReactNode;
  hint?: ReactNode;
  disabled?: boolean;
  name?: string;
}) => (
  <label
    className={`flex items-start gap-3 py-1.5 ${disabled ? "opacity-50" : "cursor-pointer"}`}
  >
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      disabled={disabled}
      onClick={() => onChange?.(!checked)}
      className={`mt-0.5 flex h-4.5 w-8 shrink-0 items-center rounded-full px-0.5 transition-colors duration-100 ${
        checked ? "bg-brand" : "bg-line-strong"
      }`}
    >
      <span
        className={`size-3.5 rounded-full bg-white transition-transform duration-100 ${
          checked ? "translate-x-3.5" : "translate-x-0"
        }`}
      />
    </button>
    <span className="min-w-0">
      <span className="block text-sm text-ink">{label}</span>
      {hint && <span className="block text-xs text-ink-3">{hint}</span>}
    </span>
    {name && <input type="hidden" name={name} value={checked ? "on" : ""} />}
  </label>
);
