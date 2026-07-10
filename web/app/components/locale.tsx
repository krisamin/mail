import { useLocation, useSubmit } from "react-router";
import { LOCALE_LABEL_MAP, LOCALE_LIST } from "~/lib/locale";
import { useI18n } from "~/lib/i18n";

/** Language selector — posts to /locale (cookie) and revalidates in place. */
export const LocaleSwitch = ({ className = "" }: { className?: string }) => {
  const { locale, t } = useI18n();
  const submit = useSubmit();
  const location = useLocation();

  return (
    <select
      aria-label={t("locale.label")}
      value={locale}
      onChange={(e) =>
        submit(
          { locale: e.target.value, returnTo: location.pathname + location.search },
          { method: "post", action: "/locale" },
        )
      }
      className={`rounded-md border border-line bg-bg-1 px-2 py-1 text-xs text-text-2 outline-none hover:text-text-1 ${className}`}
    >
      {LOCALE_LIST.map((l) => (
        <option key={l} value={l}>
          {LOCALE_LABEL_MAP[l]}
        </option>
      ))}
    </select>
  );
};
