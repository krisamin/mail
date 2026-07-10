import { CopyButton } from "./copy";

/** One-time secret display (app passwords) — shown once, copy button + select-all. */
export const SecretReveal = ({ title, value }: { title: string; value: string }) => (
  <div className="rounded-md border border-warn/40 bg-warn/10 p-4" role="alert">
    <div className="flex items-center justify-between">
      <p className="text-sm font-medium text-warn">{title}</p>
      <CopyButton value={value} />
    </div>
    <p className="mt-2 select-all rounded bg-bg-0 p-2 text-center font-mono text-lg tracking-wider text-text-0">
      {value}
    </p>
  </div>
);
