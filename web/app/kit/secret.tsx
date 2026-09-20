import { useT } from "~/lib/i18n";
import { Banner } from "./banner";
import { CopyButton } from "./copy";

/** One-time secret display — app passwords are never shown again. */
export const SecretReveal = ({ title, value }: { title: string; value: string }) => {
  const t = useT();
  return (
    <Banner tone="ok">
      <p className="font-medium">{title}</p>
      <p className="mt-1 text-xs opacity-80">{t("common.secretOnce")}</p>
      <div className="mt-2 flex items-center gap-2">
        <code className="min-w-0 flex-1 truncate rounded bg-canvas/60 px-2 py-1.5 font-mono text-xs text-ink">
          {value}
        </code>
        <CopyButton value={value} />
      </div>
    </Banner>
  );
};
