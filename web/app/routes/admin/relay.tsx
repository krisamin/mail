import { Form, useNavigation } from "react-router";
import type { Route } from "./+types/relay";
import { ApiError, apiFetch, type Domain, type Relay } from "~/lib/api.server";
import { getUser } from "~/lib/session.server";

// relay 관리 — 발송 SMTP relay를 DB로 관리 (env 하드코딩 탈피).
// password는 쓰기 전용: 서버가 절대 안 돌려줌 (hasPassword 배지만).

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = (await getUser(request))!;
  const [relayList, domainList] = await Promise.all([
    apiFetch<Relay[]>(user.idToken, "/api/admin/relay"),
    apiFetch<Domain[]>(user.idToken, "/api/admin/domain"),
  ]);
  return { relayList: relayList ?? [], domainList: domainList ?? [] };
};

export const action = async ({ request }: Route.ActionArgs) => {
  const user = (await getUser(request))!;
  const form = await request.formData();
  const intent = form.get("intent");

  try {
    switch (intent) {
      case "create": {
        await apiFetch(user.idToken, "/api/admin/relay", {
          method: "POST",
          body: {
            name: String(form.get("name") ?? ""),
            host: String(form.get("host") ?? ""),
            port: Number(form.get("port") ?? 587),
            username: String(form.get("username") ?? ""),
            password: String(form.get("password") ?? ""),
            starttls: form.get("starttls") === "on",
            isDefault: form.get("isDefault") === "on",
          },
        });
        return { ok: true as const };
      }
      case "update": {
        await apiFetch(user.idToken, `/api/admin/relay/${form.get("id")}`, {
          method: "PUT",
          body: {
            name: String(form.get("name") ?? ""),
            host: String(form.get("host") ?? ""),
            port: Number(form.get("port") ?? 587),
            username: String(form.get("username") ?? ""),
            // 빈 문자열 = 기존 비밀번호 유지
            password: String(form.get("password") ?? ""),
            starttls: form.get("starttls") === "on",
            isDefault: form.get("isDefault") === "on",
            active: form.get("active") === "on",
          },
        });
        return { ok: true as const };
      }
      case "delete": {
        await apiFetch(user.idToken, `/api/admin/relay/${form.get("id")}`, {
          method: "DELETE",
        });
        return { ok: true as const };
      }
      case "assign": {
        const relayIdRaw = String(form.get("relayId") ?? "");
        await apiFetch(user.idToken, `/api/admin/domain/${form.get("domainId")}/relay`, {
          method: "PUT",
          body: { relayId: relayIdRaw === "" ? null : Number(relayIdRaw) },
        });
        return { ok: true as const };
      }
      default:
        return { ok: false as const, error: "알 수 없는 요청" };
    }
  } catch (e) {
    if (e instanceof ApiError) return { ok: false as const, error: e.message };
    throw e;
  }
};

const inputCls =
  "rounded-md border border-line bg-bg-1 px-3 py-2 text-sm outline-none focus:border-accent";

export default function RelayList({ loaderData, actionData }: Route.ComponentProps) {
  const { relayList, domainList } = loaderData;
  const nav = useNavigation();
  const busy = nav.state !== "idle";

  return (
    <div className="flex flex-col gap-6">
      <h1 className="text-xl font-bold">발송 relay</h1>
      <p className="text-sm text-text-2">
        외부 도메인으로 나가는 메일이 경유할 SMTP relay. 서버에 있는 도메인끼리는 relay를 거치지
        않고 내부 배달돼요. 도메인별 지정이 없으면 <b>기본 relay</b>를 사용.
      </p>

      {actionData && !actionData.ok && (
        <p className="rounded-md border border-bad/40 bg-bad/10 px-3 py-2 text-sm text-bad">
          {actionData.error}
        </p>
      )}

      {/* 새 relay */}
      <Form method="post" className="flex flex-col gap-2 rounded-md border border-line bg-bg-1 p-4">
        <input type="hidden" name="intent" value="create" />
        <p className="text-sm font-medium">새 relay</p>
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
          <input name="name" required placeholder="이름 (resend)" className={inputCls} />
          <input name="host" required placeholder="smtp.resend.com" className={inputCls} />
          <input name="port" type="number" defaultValue={587} className={inputCls} />
          <input name="username" placeholder="username" className={inputCls} />
          <input
            name="password"
            type="password"
            placeholder="password / API key"
            className={`${inputCls} col-span-2`}
          />
          <label className="flex items-center gap-1.5 text-xs text-text-2">
            <input type="checkbox" name="starttls" defaultChecked /> STARTTLS
          </label>
          <label className="flex items-center gap-1.5 text-xs text-text-2">
            <input type="checkbox" name="isDefault" /> 기본 relay
          </label>
        </div>
        <button
          type="submit"
          disabled={busy}
          className="self-start rounded-md bg-accent px-4 py-2 text-sm font-medium text-bg-0 hover:bg-accent-hover disabled:opacity-50"
        >
          추가
        </button>
      </Form>

      {/* relay 목록 */}
      <div className="rounded-md border border-line bg-bg-1">
        {relayList.length === 0 ? (
          <p className="px-4 py-6 text-center text-sm text-text-2">
            relay 없음 — 외부 발송은 큐에 쌓였다가 relay를 추가하면 나가요
          </p>
        ) : (
          <ul className="divide-y divide-line">
            {relayList.map((r) => (
              <li key={r.id} className="px-4 py-3">
                <Form method="post" className="flex flex-col gap-2">
                  <input type="hidden" name="intent" value="update" />
                  <input type="hidden" name="id" value={r.id} />
                  <div className="flex items-center gap-2">
                    <span className="text-sm font-medium">{r.name}</span>
                    {r.isDefault && (
                      <span className="rounded bg-accent-soft px-1.5 py-0.5 text-[10px] text-accent">
                        기본
                      </span>
                    )}
                    {!r.active && (
                      <span className="rounded bg-bg-3 px-1.5 py-0.5 text-[10px] text-muted">
                        비활성
                      </span>
                    )}
                    <span className="text-xs text-text-2">
                      {r.host}:{r.port}
                    </span>
                  </div>
                  <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
                    <input name="name" defaultValue={r.name} className={inputCls} />
                    <input name="host" defaultValue={r.host} className={inputCls} />
                    <input name="port" type="number" defaultValue={r.port} className={inputCls} />
                    <input name="username" defaultValue={r.username} className={inputCls} />
                    <input
                      name="password"
                      type="password"
                      placeholder={r.hasPassword ? "(설정됨 — 비우면 유지)" : "password / API key"}
                      className={`${inputCls} col-span-2`}
                    />
                    <label className="flex items-center gap-1.5 text-xs text-text-2">
                      <input type="checkbox" name="starttls" defaultChecked={r.starttls} /> STARTTLS
                    </label>
                    <label className="flex items-center gap-1.5 text-xs text-text-2">
                      <input type="checkbox" name="isDefault" defaultChecked={r.isDefault} /> 기본
                    </label>
                    <label className="flex items-center gap-1.5 text-xs text-text-2">
                      <input type="checkbox" name="active" defaultChecked={r.active} /> 활성
                    </label>
                  </div>
                  <div className="flex gap-3">
                    <button type="submit" disabled={busy} className="text-xs text-accent hover:underline">
                      저장
                    </button>
                  </div>
                </Form>
                <Form method="post" className="mt-1">
                  <input type="hidden" name="intent" value="delete" />
                  <input type="hidden" name="id" value={r.id} />
                  <button type="submit" disabled={busy} className="text-xs text-bad hover:underline">
                    삭제
                  </button>
                </Form>
              </li>
            ))}
          </ul>
        )}
      </div>

      {/* 도메인별 relay 지정 */}
      <div className="rounded-md border border-line bg-bg-1 p-4">
        <p className="mb-3 text-sm font-medium">도메인별 발신 relay</p>
        <ul className="flex flex-col gap-2">
          {domainList.map((d) => (
            <li key={d.id}>
              <Form method="post" className="flex items-center gap-2">
                <input type="hidden" name="intent" value="assign" />
                <input type="hidden" name="domainId" value={d.id} />
                <span className="w-40 text-sm">{d.name}</span>
                <select
                  name="relayId"
                  defaultValue={d.relayId ?? ""}
                  className="rounded border border-line bg-bg-0 px-2 py-1 text-xs outline-none focus:border-accent"
                >
                  <option value="">(기본 relay)</option>
                  {relayList.map((r) => (
                    <option key={r.id} value={r.id}>
                      {r.name} — {r.host}
                    </option>
                  ))}
                </select>
                <button type="submit" disabled={busy} className="text-xs text-accent hover:underline">
                  지정
                </button>
              </Form>
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}
