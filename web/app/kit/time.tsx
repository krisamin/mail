import { useT } from "~/lib/i18n";
import { useLocale } from "~/lib/i18n";

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/** Absolute timestamp, formatted in the viewer's locale and zone. */
export const TimeText = ({
  value,
  className = "",
  mode = "auto",
}: {
  value: string;
  className?: string;
  /** auto: relative under a day, date beyond it. full: always date + time. */
  mode?: "auto" | "full";
}) => {
  const locale = useLocale();
  const t = useT();
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return null;

  const full = new Intl.DateTimeFormat(locale, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(date);

  let label = full;
  if (mode === "auto") {
    const diff = Date.now() - date.getTime();
    if (diff < MINUTE) label = t("time.now");
    else if (diff < HOUR) label = t("time.minute", { count: Math.floor(diff / MINUTE) });
    else if (diff < DAY) label = t("time.hour", { count: Math.floor(diff / HOUR) });
    else if (diff < 7 * DAY)
      label = new Intl.DateTimeFormat(locale, { weekday: "short", hour: "numeric", minute: "2-digit" }).format(date);
    else
      label = new Intl.DateTimeFormat(locale, { month: "short", day: "numeric" }).format(date);
  }

  return (
    <time dateTime={value} title={full} className={className}>
      {label}
    </time>
  );
};
