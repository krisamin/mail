import type { InputHTMLAttributes, ReactNode, SelectHTMLAttributes, TextareaHTMLAttributes } from "react";

const controlClass =
  "w-full rounded-md border border-line bg-surface px-3 text-sm text-ink placeholder:text-ink-faint transition-colors duration-100 hover:border-line-strong focus:border-brand focus:outline-none disabled:opacity-50";

const heightMap = { md: "h-9", sm: "h-8 text-xs" } as const;
export type FieldSize = keyof typeof heightMap;

export const TextInput = ({
  fieldSize = "md",
  className = "",
  ...rest
}: InputHTMLAttributes<HTMLInputElement> & { fieldSize?: FieldSize }) => (
  <input {...rest} className={`${controlClass} ${heightMap[fieldSize]} ${className}`} />
);

export const TextArea = ({
  className = "",
  ...rest
}: TextareaHTMLAttributes<HTMLTextAreaElement>) => (
  <textarea {...rest} className={`${controlClass} py-2 leading-6 ${className}`} />
);

export const SelectInput = ({
  fieldSize = "md",
  className = "",
  children,
  ...rest
}: SelectHTMLAttributes<HTMLSelectElement> & { fieldSize?: FieldSize }) => (
  <select {...rest} className={`${controlClass} ${heightMap[fieldSize]} pr-8 ${className}`}>
    {children}
  </select>
);

export const CheckboxLabel = ({
  name,
  defaultChecked,
  checked,
  onChange,
  children,
}: {
  name?: string;
  defaultChecked?: boolean;
  checked?: boolean;
  onChange?: InputHTMLAttributes<HTMLInputElement>["onChange"];
  children: ReactNode;
}) => (
  <label className="inline-flex cursor-pointer items-center gap-2 text-sm text-ink-2">
    <input
      type="checkbox"
      name={name}
      defaultChecked={defaultChecked}
      checked={checked}
      onChange={onChange}
      className="size-4 accent-[var(--tone-brand)]"
    />
    {children}
  </label>
);

/** Label + control + optional hint, stacked. */
export const Field = ({
  label,
  hint,
  htmlFor,
  children,
}: {
  label: string;
  hint?: ReactNode;
  htmlFor?: string;
  children: ReactNode;
}) => (
  <div className="flex flex-col gap-1.5">
    <label htmlFor={htmlFor} className="text-xs font-medium text-ink-2">
      {label}
    </label>
    {children}
    {hint && <p className="text-xs text-ink-3">{hint}</p>}
  </div>
);
