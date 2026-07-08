import { Form, Link, useNavigation } from "react-router";
import type { Route } from "./+types/account";
import {
  ApiError,
  apiFetch,
  type Alias,
  type AppPassword,
  type Domain,
  type Account,
} from "~/lib/api.server";
import { getUser } from "~/lib/session.server";

export const loader = async ({ request, params }: Route.LoaderArgs) => {
  const user = (await getUser(request))!;
  const domainId = params.domainId;

  const [domainList, accountList, aliasList] = await Promise.all([
    apiFetch<Domain[]>(user.idToken, "/api/admin/domain"),
    apiFetch<Account[]>(user.idToken, `/api/admin/domain/${domainId}/account`),
    apiFetch<Alias[]>(user.idToken, `/api/admin/domain/${domainId}/alias`),
  ]);
  const domain = (domainList ?? []).find((d) => String(d.id) === domainId);
  if (!domain) throw new Response("도메인을 찾을 수 없어요", { status: 404 });

  // 유저별 앱비번 목록 (관리 화면이라 N+1 허용 — 유저 수 적음)
  const appPasswordList: Record<number, AppPassword[]> = {};
  await Promise.all(
    (accountList ?? []).map(async (u) => {
      appPasswordList[u.id] =
        (await apiFetch<AppPassword[]>(user.idToken, `/api/admin/account/${u.id}/app-password`)) ?? [];
    }),
  );
  return { domain, accountList: accountList ?? [], aliasList: aliasList ?? [], appPasswordList };
};

export const action = async ({ request, params }: Route.ActionArgs) => {
  const user = (await getUser(request))!;
  const form = await request.formData();
  const intent = form.get("intent");

  try {
    switch (intent) {
      case "create-user": {
        await apiFetch(user.idToken, `/api/admin/domain/${params.domainId}/account`, {
          method: "POST",
          body: { localPart: String(form.get("localPart") ?? "") },
        });
        return { ok: true as const };
      }
      case "toggle-user": {
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
      case "create-alias": {
        await apiFetch(user.idToken, `/api/admin/domain/${params.domainId}/alias`, {
          method: "POST",
          body: {
            localPart: String(form.get("localPart") ?? ""),
            accountId: Number(form.get("accountId")),
          },
        });
        return { ok: true as const };
      }
      case "delete-alias": {
        await apiFetch(user.idToken, `/api/admin/alias/${form.get("id")}`, {
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
  const { domain, accountList, aliasList, appPasswordList } = loaderData;
  const nav = useNavigation();
  const busy = nav.state !== "idle";

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center gap-2">
        <Link to="/admin/domain" className="text-sm text-text-2 hover:text-text-1">
          도메인
        </Link>
        <span className="text-text-2">/</span>
        <h1 className="text-xl font-bold">{domain.name}</h1>
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

      <Form method="post" className="flex gap-2">
        <input type="hidden" name="intent" value="create-user" />
        <div className="flex flex-1 items-center gap-1 rounded-md border border-line bg-bg-1 px-3">
          <input
            name="localPart"
            required
            placeholder="maro"
            className="flex-1 bg-transparent py-2 text-sm outline-none"
          />
          <span className="text-sm text-text-2">@{domain.name}</span>
        </div>
        <button
          type="submit"
          disabled={busy}
          className="rounded-md bg-accent px-4 py-2 text-sm font-medium text-bg-0 hover:bg-accent-hover disabled:opacity-50"
        >
          추가
        </button>
      </Form>

      <div className="flex flex-col gap-3">
        {accountList.length === 0 ? (
          <p className="rounded-md border border-line bg-bg-1 px-4 py-6 text-center text-sm text-text-2">
            유저 없음
          </p>
        ) : (
          accountList.map((u) => (
            <div key={u.id} className="rounded-md border border-line bg-bg-1">
              <div className="flex items-center justify-between border-b border-line px-4 py-2.5">
                <p className="text-sm font-medium">
                  {u.localPart}
                  <span className="text-text-2">@{domain.name}</span>
                </p>
                <Form method="post">
                  <input type="hidden" name="intent" value="toggle-user" />
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

      {/* ── 별칭 (추가 수신 주소 + catch-all) ─────────────────── */}
      <section className="flex flex-col gap-3">
        <div>
          <h2 className="text-sm font-medium text-text-1">별칭</h2>
          <p className="mt-0.5 text-xs text-text-2">
            추가 수신 주소를 유저에게 연결해요. local part에 <code className="rounded bg-bg-3 px-1">*</code>를
            넣으면 catch-all (이 도메인의 모든 미지정 주소).
          </p>
        </div>

        <Form method="post" className="flex gap-2">
          <input type="hidden" name="intent" value="create-alias" />
          <div className="flex flex-1 items-center gap-1 rounded-md border border-line bg-bg-1 px-3">
            <input
              name="localPart"
              required
              placeholder="hello 또는 *"
              className="flex-1 bg-transparent py-2 text-sm outline-none"
            />
            <span className="text-sm text-text-2">@{domain.name}</span>
            <span className="text-sm text-text-2">→</span>
            <select
              name="accountId"
              required
              className="rounded border border-line bg-bg-0 px-2 py-1 text-sm outline-none"
            >
              {accountList.map((u) => (
                <option key={u.id} value={u.id}>
                  {u.localPart}@{domain.name}
                </option>
              ))}
            </select>
          </div>
          <button
            type="submit"
            disabled={busy || accountList.length === 0}
            className="rounded-md bg-accent px-4 py-2 text-sm font-medium text-bg-0 hover:bg-accent-hover disabled:opacity-50"
          >
            연결
          </button>
        </Form>

        <div className="rounded-md border border-line bg-bg-1">
          {aliasList.length === 0 ? (
            <p className="px-4 py-4 text-center text-xs text-text-2">별칭 없음</p>
          ) : (
            <ul className="divide-y divide-line">
              {aliasList.map((a) => (
                <li key={a.id} className="flex items-center justify-between px-4 py-2.5">
                  <div className="flex items-center gap-2">
                    <span className="font-mono text-sm text-text-0">
                      {a.localPart === "*" ? (
                        <span className="text-warn">*</span>
                      ) : (
                        a.localPart
                      )}
                      <span className="text-text-2">@{a.domainName}</span>
                    </span>
                    {a.localPart === "*" && (
                      <span className="rounded bg-warn/15 px-1.5 py-0.5 text-[10px] text-warn">
                        catch-all
                      </span>
                    )}
                    <span className="text-xs text-text-2">
                      → {a.accountLocalPart}@{a.accountDomainName}
                    </span>
                  </div>
                  <Form method="post">
                    <input type="hidden" name="intent" value="delete-alias" />
                    <input type="hidden" name="id" value={a.id} />
                    <button type="submit" disabled={busy} className="text-[10px] text-bad hover:underline">
                      삭제
                    </button>
                  </Form>
                </li>
              ))}
            </ul>
          )}
        </div>
      </section>
    </div>
  );
}
