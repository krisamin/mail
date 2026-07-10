import { useI18n } from "~/lib/i18n";

/** Locale-aware timestamp — renders an ISO/UTC string in the viewer's locale
 *  instead of raw "2026-07-10T06:00:00Z" (KST users saw times 9h off). */
export const TimeText = ({ value, className = "" }: { value: string; className?: string }) => {
  const { locale } = useI18n();
  const d = new Date(value);
  const text = Number.isNaN(d.getTime())
    ? value
    : new Intl.DateTimeFormat(locale, { dateStyle: "short", timeStyle: "medium" }).format(d);
  return (
    <time dateTime={value} suppressHydrationWarning className={className}>
      {text}
    </time>
  );
};
