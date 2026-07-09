import type { InputHTMLAttributes, SelectHTMLAttributes } from "react";

// Form fields — md for standalone forms, sm for inline (inside list rows).
const inputSize = {
  md: "rounded-md border border-line bg-bg-1 px-3 py-2 text-sm",
  sm: "rounded border border-line bg-bg-0 px-2 py-0.5 text-xs",
} as const;

const selectSize = {
  md: "rounded-md border border-line bg-bg-1 px-2 py-2 text-sm",
  sm: "rounded border border-line bg-bg-0 px-2 py-0.5 text-xs",
} as const;

type FieldSize = keyof typeof inputSize;

export const TextInput = ({
  fieldSize = "md",
  className = "",
  ...props
}: Omit<InputHTMLAttributes<HTMLInputElement>, "size"> & { fieldSize?: FieldSize }) => (
  <input
    {...props}
    className={`${inputSize[fieldSize]} outline-none focus:border-accent ${className}`}
  />
);

export const SelectInput = ({
  fieldSize = "md",
  className = "",
  ...props
}: Omit<SelectHTMLAttributes<HTMLSelectElement>, "size"> & { fieldSize?: FieldSize }) => (
  <select
    {...props}
    className={`${selectSize[fieldSize]} outline-none focus:border-accent ${className}`}
  />
);

export const CheckboxLabel = ({
  label,
  ...props
}: InputHTMLAttributes<HTMLInputElement> & { label: string }) => (
  <label className="flex items-center gap-1.5 text-xs text-text-2">
    <input type="checkbox" {...props} /> {label}
  </label>
);
