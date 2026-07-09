import { Form, useNavigation } from "react-router";
import type { Route } from "./+types/account";
import {
  ApiError,
  apiFetch,
  type Address,
  type AppPassword,
  type Account,
} from "~/lib/api.server";
import { getUser } from "~/lib/session.server";

// 계정 관리 — 전체 계정 목록 + 계정별 주소/앱 비밀번호 (admin 전용).
// 계정은 JIT 프로비저닝(첫 OIDC 로그인)으로 생긴다 — 여기선 생성 불가.
// 주소 연결은 도메인 페이지(/admin/domain/:id/address)에서.

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = (await getUser(request))!;

  const accountList = (await apiFetch<Account[]>(user.idToken, "/api/admin/account")) ?? [];

  // 계정별 주소/앱비번 (관리 화면이라 N+1 허용 — 계정 수 적음)
  const addressList: Record<number, Address[]> = {};
  const appPasswordList: Record<number, AppPassword[]> = {};
  await Promise.all(
    accountList.map(async (u) => {
      [addressList[u.id], appPasswordList[u.id]] = await Promise.all([
        apiFetch<Address[]>(user.idToken, `/api/admin/account/${u.id}/address`).then((r) => r ?? []),
        apiFetch<AppPassword[]>(user.idToken, `/api/admin/account/${u.id}/app-password`).then(
          (r) => r ?? [],
        ),
      ]);
    }),
  );
  return { accountList, addressList, appPasswordList };
};

export const action = async ({ request }: Route.ActionArgs) => {
  const user = (await getUser(request))!;
  const form = await request.formData();
  const intent = form.get("intent");

  try {
    switch (intent) {
      case "toggle-account": {
        await apiFetch(user.idToken, `/api/admin/account/${form.get("id")}`, {
          method: "PATCH",
          body: { active: form.get("active") === "true" },
        });
        return { ok: true as const };
      }
      case "create-pw": {
        const result = await apiFetch<{ appPassword: AppPassword; plaintext: string }>(
          user.idToken,
          `/api/admin/account/${form.get("accountId")}/app-password`,
          { method: "POST", body: { label: String(form.get("label") ?? "") } },
        );
        return { ok: true as const, plaintext: result.plaintext, accountId: Number(form.get("accountId")) };
      }
      case "revoke-pw": {
        await apiFetch(user.idToken, `/api/admin/app-password/${form.get("id")}`, {
          method: "DELETE",
        });
        return { ok: true as const };
      }
      case "delete-address": {
        await apiFetch(user.idToken, `/api/admin/address/${form.get("id")}`, {
          method: "DELETE",
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

export default function AccountList({ loaderData, actionData }: Route.ComponentProps) {
  const { accountList, addressList, appPasswordList } = loaderData;
  const nav = useNavigation();
  const busy = nav.state !== "idle";

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-xl font-bold">계정</h1>
        <p className="mt-0.5 text-xs text-text-2">
          계정은 유저가 처음 로그인할 때 자동으로 생겨요 (OIDC 신원 기준). 주소 연결은 각 도메인
          페이지에서.
        </p>
      </div>

      {actionData && !actionData.ok && (
        <p className="rounded-md border border-bad/40 bg-bad/10 px-3 py-2 text-sm text-bad">
          {actionData.error}
        </p>
      )}

      {actionData?.ok && "plaintext" in actionData && actionData.plaintext && (
        <div className="rounded-md border border-warn/40 bg-warn/10 p-4">
          <p className="text-sm font-medium text-warn">앱 비밀번호 — 지금만 표시됨</p>
          <p className="mt-2 select-all rounded bg-bg-0 p-2 text-center font-mono text-lg tracking-wider text-text-0">
            {actionData.plaintext}
          </p>
        </div>
      )}

      <div className="flex flex-col gap-3">
        {accountList.length === 0 ? (
          <p className="rounded-md border border-line bg-bg-1 px-4 py-6 text-center text-sm text-text-2">
            계정 없음 — 유저가 로그인하면 여기 나타나요.
          </p>
        ) : (
          accountList.map((u) => (
            <div key={u.id} className="rounded-md border border-line bg-bg-1">
              <div className="flex items-center justify-between border-b border-line px-4 py-2.5">
                <div>
                  <p className="text-sm font-medium">{u.email}</p>
                  <p className="font-mono text-[10px] text-text-2">sub: {u.subject}</p>
                </div>
                <Form method="post">
                  <input type="hidden" name="intent" value="toggle-account" />
                  <input type="hidden" name="id" value={u.id} />
                  <input type="hidden" name="active" value={String(!u.active)} />
                  <button
                    type="submit"
                    disabled={busy}
                    className={`rounded px-2 py-1 text-xs ${
                      u.active ? "bg-ok/20 text-ok hover:bg-ok/30" : "bg-bg-3 text-muted hover:bg-bg-2"
                    }`}
                  >
                    {u.active ? "활성" : "비활성"}
                  </button>
                </Form>
              </div>

              <div className="flex flex-col gap-2 px-4 py-3">
                <p className="text-xs text-text-2">주소</p>
                {(addressList[u.id] ?? []).length === 0 ? (
                  <p className="text-xs text-muted">주소 없음</p>
                ) : (
                  <ul className="flex flex-wrap gap-1.5">
                    {(addressList[u.id] ?? []).map((a) => (
                      <li
                        key={a.id}
                        className="flex items-center gap-1.5 rounded bg-bg-3 px-2 py-0.5 font-mono text-xs text-text-1"
                      >
                        {a.localPart === "*" ? <span className="text-warn">*</span> : a.localPart}@
                        {a.domainName}
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
                      </li>
                    ))}
                  </ul>
                )}
              </div>

              <div className="flex flex-col gap-2 border-t border-line px-4 py-3">
                <div className="flex items-center justify-between">
                  <p className="text-xs text-text-2">앱 비밀번호</p>
                  <Form method="post" className="flex items-center gap-1.5">
                    <input type="hidden" name="intent" value="create-pw" />
                    <input type="hidden" name="accountId" value={u.id} />
                    <input
                      name="label"
                      placeholder="라벨 (예: Thunderbird)"
                      className="w-40 rounded border border-line bg-bg-0 px-2 py-0.5 text-xs outline-none focus:border-accent"
                    />
                    <button type="submit" disabled={busy} className="text-xs text-accent hover:underline">
                      발급
                    </button>
                  </Form>
                </div>
                {(appPasswordList[u.id] ?? []).length > 0 && (
                  <ul className="divide-y divide-line/50">
                    {(appPasswordList[u.id] ?? []).map((p) => (
                      <li key={p.id} className="flex items-center justify-between py-1.5">
                        <div className="flex items-center gap-2">
                          <span className={`text-xs ${p.revoked ? "text-muted line-through" : "text-text-1"}`}>
                            {p.label}
                          </span>
                          <span className="text-[10px] text-text-2">
                            {p.lastUsed ? `마지막 사용 ${p.lastUsed.slice(0, 10)}` : "미사용"}
                          </span>
                        </div>
                        {!p.revoked && (
                          <Form method="post">
                            <input type="hidden" name="intent" value="revoke-pw" />
                            <input type="hidden" name="id" value={p.id} />
                            <button type="submit" disabled={busy} className="text-[10px] text-bad hover:underline">
                              revoke
                            </button>
                          </Form>
                        )}
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            </div>
          ))
        )}
      </div>
    </div>
  );
}
