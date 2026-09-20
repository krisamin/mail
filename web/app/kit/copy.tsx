import { useState } from "react";
import { useT } from "~/lib/i18n";
import { CheckIcon, CopyIcon } from "./icon";
import { Button } from "./button";

/** Copy-to-clipboard button with a short confirmation. */
export const CopyButton = ({
  value,
  label,
  size = "sm",
}: {
  value: string;
  label?: string;
  size?: "sm" | "md";
}) => {
  const t = useT();
  const [copied, setCopied] = useState(false);
  return (
    <Button
      type="button"
      variant="subtle"
      size={size}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(value);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        } catch {
          // clipboard blocked (insecure origin) — leave the label unchanged
        }
      }}
    >
      {copied ? <CheckIcon className="size-3.5" /> : <CopyIcon className="size-3.5" />}
      <span aria-live="polite">{copied ? t("common.copied") : (label ?? t("common.copy"))}</span>
    </Button>
  );
};
