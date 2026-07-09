import { Form } from "react-router";
import type { Address, AppPassword } from "~/lib/api.server";

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
}) =>
  list.length === 0 ? (
    <p className="text-xs text-muted">주소 없음</p>
  ) : (
    <ul className="flex flex-wrap gap-1.5">
      {list.map((a) => (
        <li
          key={a.id}
          className="flex items-center gap-1.5 rounded bg-bg-3 px-2 py-0.5 font-mono text-xs text-text-1"
        >
          {a.localPart === "*" ? <span className="text-warn">*</span> : a.localPart}@{a.domainName}
          {deletable && (
            <Form method="post" className="inline">
              <input type="hidden" name="intent" value="delete-address" />
              <input type="hidden" name="id" value={a.id} />
              <button
                type="submit"
                disabled={busy}
                className="text-[10px] text-bad hover:underline"
                title="주소 삭제"
              >
                ×
              </button>
            </Form>
          )}
        </li>
      ))}
    </ul>
  );

/**
 * App password rows with revoke buttons. Each revoke submits a Form
 * (intent=revoke-pw, id) — the page's action must handle it.
 */
export const AppPasswordRows = ({ list, busy = false }: { list: AppPassword[]; busy?: boolean }) =>
  list.length === 0 ? null : (
    <ul className="divide-y divide-line/50">
      {list.map((p) => (
        <li key={p.id} className="flex items-center justify-between py-1.5">
          <div className="flex items-center gap-2">
            <span className={`text-xs ${p.revoked ? "text-muted line-through" : "text-text-1"}`}>
              {p.label || "(라벨 없음)"}
            </span>
            <span className="text-[10px] text-text-2">
              발급 {p.createdAt.slice(0, 10)}
              {p.lastUsed ? ` · 마지막 사용 ${p.lastUsed.slice(0, 10)}` : " · 미사용"}
            </span>
          </div>
          {!p.revoked && (
            <Form method="post">
              <input type="hidden" name="intent" value="revoke-pw" />
              <input type="hidden" name="id" value={p.id} />
              <button
                type="submit"
                disabled={busy}
                className="text-[10px] text-bad hover:underline"
              >
                revoke
              </button>
            </Form>
          )}
        </li>
      ))}
    </ul>
  );
