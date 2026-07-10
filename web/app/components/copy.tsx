import { useEffect, useRef, useState } from "react";
import { useT } from "~/lib/i18n";

/** Copy-to-clipboard chip — brief "copied" feedback, no layout shift.
 *  Use next to DNS records / DKIM TXT / app passwords (long strings where
 *  manual drag-select truncates or picks up stray whitespace). */
export const CopyButton = ({ value, className = "" }: { value: string; className?: string }) => {
  const t = useT();
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current);
  }, []);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => setCopied(false), 1500);
    } catch {
      // clipboard permission denied — leave the select-all fallback to the user
    }
  };

  return (
    <button
      type="button"
      onClick={copy}
      aria-live="polite"
      className={`rounded bg-bg-3 px-2 py-1 text-xs transition-colors duration-100 ${
        copied ? "text-ok" : "text-text-2 hover:bg-bg-2"
      } ${className}`}
    >
      {copied ? t("common.copied") : t("common.copy")}
    </button>
  );
};
