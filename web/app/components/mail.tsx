import { Form } from "react-router";
import type { Address, AppPassword } from "~/lib/api.server";
import { useT } from "~/lib/i18n";

// Mail domain molecules — shared between self-service (/account) and admin pages.

/**
 * Address chips for one account. When deletable, each chip carries a delete
 * Form (intent=delete-address, id) — the page's action must handle it.
 */
export const AddressChipList = ({
  list,
  busy = false,
  deletable = false,
}: {
  list: Address[];
  busy?: boolean;
  deletable?: boolean;
}) => {
  const t = useT();
  return list.length === 0 ? (
    <p className="text-xs text-muted">{t("mail.noAddress")}</p>
  ) : (
    <ul className="flex flex-wrap gap-1.5">
      {list.map((a) => (
        <li
          key={a.id}
          className="flex items-center gap-1.5 rounded bg-bg-3 px-2 py-0.5 font-mono text-xs text-text-1"
        >
          {a.localPart === "*" ? <span className="text-warn">*</span> : a.localPart}@{a.domainName}
          {deletable && (
            <Form
              method="post"
              className="inline"
              onSubmit={(e) => {
                if (!window.confirm(t("common.confirmDelete"))) e.preventDefault();
              }}
            >
              <input type="hidden" name="intent" value="delete-address" />
              <input type="hidden" name="id" value={a.id} />
              <button
                type="submit"
                disabled={busy}
                className="px-1 text-[10px] text-bad hover:underline"
                aria-label={t("mail.deleteAddress")}
                title={t("mail.deleteAddress")}
              >
                ×
              </button>
            </Form>
          )}
        </li>
      ))}
    </ul>
  );
};

/**
 * App password rows with revoke buttons. Each revoke submits a Form
 * (intent=revoke-pw, id) — the page's action must handle it.
 */
export const AppPasswordRows = ({ list, busy = false }: { list: AppPassword[]; busy?: boolean }) => {
  const t = useT();
  return list.length === 0 ? null : (
    <ul className="divide-y divide-line/50">
      {list.map((p) => (
        <li key={p.id} className="flex items-center justify-between py-1.5">
          <div className="flex items-center gap-2">
            <span className={`text-xs ${p.revoked ? "text-muted line-through" : "text-text-1"}`}>
              {p.label || t("mail.noLabel")}
            </span>
            <span className="text-[10px] text-text-2">
              {t("mail.issuedAt", { date: p.createdAt.slice(0, 10) })}
              {p.lastUsed
                ? ` · ${t("mail.lastUsedAt", { date: p.lastUsed.slice(0, 10) })}`
                : ` · ${t("mail.neverUsed")}`}
            </span>
          </div>
          {!p.revoked && (
            <Form
              method="post"
              onSubmit={(e) => {
                if (!window.confirm(t("common.confirmRevoke"))) e.preventDefault();
              }}
            >
              <input type="hidden" name="intent" value="revoke-pw" />
              <input type="hidden" name="id" value={p.id} />
              <button
                type="submit"
                disabled={busy}
                className="text-[10px] text-bad hover:underline"
              >
                {t("mail.revoke")}
              </button>
            </Form>
          )}
        </li>
      ))}
    </ul>
  );
};
