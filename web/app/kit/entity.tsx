import { Form } from "react-router";
import type { Address, AppPassword } from "~/lib/api.server";
import { useT } from "~/lib/i18n";
import { Button } from "./button";
import { CloseIcon } from "./icon";
import { TimeText } from "./time";

// Domain molecules — small compositions that show mail entities the same way
// wherever they appear (settings and admin).

/** Address chips for one account. Deletable chips post intent=delete-address. */
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
    <p className="text-xs text-ink-faint">{t("entity.noAddress")}</p>
  ) : (
    <ul className="flex flex-wrap gap-1.5">
      {list.map((address) => (
        <li
          key={address.id}
          className="flex items-center gap-1 rounded-md bg-raised py-1 pr-1 pl-2 font-mono text-xs text-ink-2"
        >
          {address.localPart === "*" ? <span className="text-warn">*</span> : address.localPart}@
          {address.domainName}
          {deletable && (
            <Form method="post" className="inline-flex">
              <input type="hidden" name="intent" value="delete-address" />
              <input type="hidden" name="id" value={address.id} />
              <button
                type="submit"
                disabled={busy}
                aria-label={t("entity.deleteAddress")}
                title={t("entity.deleteAddress")}
                onClick={(e) => {
                  if (!window.confirm(t("common.confirmDelete"))) e.preventDefault();
                }}
                className="flex size-4 items-center justify-center rounded text-ink-faint hover:bg-bad-soft hover:text-bad"
              >
                <CloseIcon className="size-3" />
              </button>
            </Form>
          )}
        </li>
      ))}
    </ul>
  );
};

/** App password rows with revoke buttons (intent=revoke-pw). */
export const AppPasswordRow = ({ list, busy = false }: { list: AppPassword[]; busy?: boolean }) => {
  const t = useT();
  return list.length === 0 ? null : (
    <ul className="divide-y divide-line/60">
      {list.map((item) => (
        <li key={item.id} className="flex items-center justify-between gap-3 py-2">
          <div className="min-w-0">
            <p className={`truncate text-xs ${item.revoked ? "text-ink-faint line-through" : "text-ink-2"}`}>
              {item.label || t("entity.noLabel")}
            </p>
            <p className="text-[11px] text-ink-faint">
              <TimeText value={item.createdAt} />
              {item.lastUsed ? (
                <>
                  {" · "}
                  {t("entity.lastUsed")} <TimeText value={item.lastUsed} />
                </>
              ) : (
                <> {" · "}{t("entity.neverUsed")}</>
              )}
            </p>
          </div>
          {!item.revoked && (
            <Form method="post">
              <input type="hidden" name="intent" value="revoke-pw" />
              <input type="hidden" name="id" value={item.id} />
              <Button variant="danger" size="sm" disabled={busy} confirmMessage={t("common.confirmRevoke")}>
                {t("entity.revoke")}
              </Button>
            </Form>
          )}
        </li>
      ))}
    </ul>
  );
};
