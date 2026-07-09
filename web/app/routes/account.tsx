import { Form, Link, redirect, useNavigation } from "react-router";
import type { Route } from "./+types/account";
import { ApiError, apiFetch, type Address, type AppPassword, type Account } from "~/lib/api.server";
import { getUser, isAdmin } from "~/lib/session.server";

// 셀프서비스 — 로그인한 유저 본인의 메일 계정 + 앱 비밀번호 관리.
// admin 그룹 불필요. Go API가 sub 클레임으로 본인 계정을 매핑한다.
// 계정은 첫 로그인 때 JIT 프로비저닝으로 생기고, 주소 추가는 admin 전용.

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await getUser(request);
  if (!user) {
    throw redirect(`/login?returnTo=${encodeURIComponent("/account")}`);
  }

  let account: Account | null = null;
  let appPasswordList: AppPassword[] = [];
  let addressList: Address[] = [];
  let noAccount = false;
  try {
    account = await apiFetch<Account>(user.idToken, "/api/me/account");
    [appPasswordList, addressList] = await Promise.all([
      apiFetch<AppPassword[]>(user.idToken, "/api/me/app-password").then((r) => r ?? []),
      apiFetch<Address[]>(user.idToken, "/api/me/address").then((r) => r ?? []),
    ]);
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) {
      noAccount = true; // 프로비저닝 전 상태 (정상 로그인이면 없을 일)
    } else {
      throw e;
    }
  }
  return {
    name: user.name,
    email: user.email,
    admin: isAdmin(user),
    account,
    appPasswordList,
    addressList,
    noAccount,
  };
};

export const action = async ({ request }: Route.ActionArgs) => {
  const user = await getUser(request);
  if (!user) throw redirect("/login");
  const form = await request.formData();
  const intent = form.get("intent");

  try {
    switch (intent) {
      case "create-pw": {
        const result = await apiFetch<{ appPassword: AppPassword; plaintext: string }>(
          user.idToken,
          "/api/me/app-password",
          { method: "POST", body: { label: String(form.get("label") ?? "") } },
        );
        return { ok: true as const, plaintext: result.plaintext };
      }
      case "revoke-pw": {
        await apiFetch(user.idToken, `/api/me/app-password/${form.get("id")}`, {
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

export default function Account({ loaderData, actionData }: Route.ComponentProps) {
  const { name, email, admin, account, appPasswordList, addressList, noAccount } = loaderData;
  const nav = useNavigation();
  const busy = nav.state !== "idle";
  const active = appPasswordList.filter((p) => !p.revoked);
  const revoked = appPasswordList.filter((p) => p.revoked);

  return (
    <div className="min-h-dvh">
      <header className="border-b border-line bg-bg-1">
        <div className="mx-auto flex max-w-3xl items-center justify-between px-4 py-3">
          <Link to="/" className="text-sm font-bold tracking-tight">
            mail <span className="text-accent">account</span>
          </Link>
          <div className="flex items-center gap-3">
            {admin && (
              <Link to="/admin" className="text-xs text-text-2 hover:text-text-1">
                관리 콘솔
              </Link>
            )}
            <span className="text-xs text-text-2">{name}</span>
            <Link to="/logout" className="text-xs text-text-2 hover:text-text-1">
              로그아웃
            </Link>
          </div>
        </div>
      </header>

      <main className="mx-auto flex max-w-3xl flex-col gap-6 px-4 py-6">
        {noAccount ? (
          <div className="rounded-md border border-line bg-bg-1 p-6 text-center">
            <p className="text-sm text-text-1">
              <span className="font-mono">{email}</span> 에 연결된 메일 계정이 아직 없어요.
            </p>
            <p className="mt-1 text-xs text-text-2">다시 로그인하면 자동으로 만들어져요.</p>
          </div>
        ) : (
          <>
            <section className="rounded-md border border-line bg-bg-1 p-4">
              <h1 className="text-lg font-bold">내 메일 계정</h1>
              <p className="mt-1 font-mono text-sm text-text-1">{email}</p>
              {addressList.length > 0 && (
                <div className="mt-2">
                  <p className="text-xs text-text-2">내 메일 주소 — 이 주소들로 받고 보낼 수 있어요:</p>
                  <ul className="mt-1 flex flex-wrap gap-1.5">
                    {addressList.map((a) => (
                      <li
                        key={a.id}
                        className="rounded bg-bg-3 px-2 py-0.5 font-mono text-xs text-text-1"
                      >
                        {a.localPart === "*" ? `*(모든 주소)` : a.localPart}@{a.domainName}
                      </li>
                    ))}
                  </ul>
                  <p className="mt-1.5 text-xs text-text-2">주소 추가는 관리자에게 요청해 주세요.</p>
                </div>
              )}
              <p className="mt-2 text-xs text-text-2">
                IMAP/SMTP 접속에는 아래에서 발급한 앱 비밀번호를 사용해요 (OIDC 비밀번호 아님).
              </p>
            </section>

            {actionData && !actionData.ok && (
              <p className="rounded-md border border-bad/40 bg-bad/10 px-3 py-2 text-sm text-bad">
                {actionData.error}
              </p>
            )}

            {actionData?.ok && "plaintext" in actionData && actionData.plaintext && (
              <div className="rounded-md border border-warn/40 bg-warn/10 p-4">
                <p className="text-sm font-medium text-warn">
                  새 앱 비밀번호 — 지금만 표시돼요. 메일 앱에 바로 붙여넣으세요.
                </p>
                <p className="mt-2 select-all rounded bg-bg-0 p-2 text-center font-mono text-lg tracking-wider text-text-0">
                  {actionData.plaintext}
                </p>
              </div>
            )}

            <section className="flex flex-col gap-3">
              <h2 className="text-sm font-medium text-text-1">앱 비밀번호</h2>
              <Form method="post" className="flex gap-2">
                <input type="hidden" name="intent" value="create-pw" />
                <input
                  name="label"
                  required
                  placeholder="라벨 (예: Thunderbird 노트북)"
                  className="flex-1 rounded-md border border-line bg-bg-1 px-3 py-2 text-sm outline-none"
                />
                <button
                  type="submit"
                  disabled={busy}
                  className="rounded-md bg-accent px-4 py-2 text-sm font-medium text-bg-0 hover:bg-accent-hover disabled:opacity-50"
                >
                  발급
                </button>
              </Form>

              {active.length === 0 && (
                <p className="rounded-md border border-line bg-bg-1 p-4 text-center text-sm text-text-2">
                  활성 앱 비밀번호가 없어요. 위에서 발급해 주세요.
                </p>
              )}
              {active.map((p) => (
                <div
                  key={p.id}
                  className="flex items-center justify-between rounded-md border border-line bg-bg-1 px-4 py-3"
                >
                  <div>
                    <p className="text-sm text-text-0">{p.label || "(라벨 없음)"}</p>
                    <p className="text-xs text-text-2">
                      발급 {p.createdAt.slice(0, 10)}
                      {p.lastUsed ? ` · 마지막 사용 ${p.lastUsed.slice(0, 10)}` : " · 사용 이력 없음"}
                    </p>
                  </div>
                  <Form method="post">
                    <input type="hidden" name="intent" value="revoke-pw" />
                    <input type="hidden" name="id" value={p.id} />
                    <button
                      type="submit"
                      disabled={busy}
                      className="rounded-md border border-bad/40 px-3 py-1.5 text-xs text-bad hover:bg-bad/10 disabled:opacity-50"
                    >
                      해제
                    </button>
                  </Form>
                </div>
              ))}

              {revoked.length > 0 && (
                <details className="text-xs text-text-2">
                  <summary className="cursor-pointer">해제된 비밀번호 {revoked.length}개</summary>
                  <ul className="mt-2 flex flex-col gap-1">
                    {revoked.map((p) => (
                      <li key={p.id} className="rounded border border-line bg-bg-1 px-3 py-2">
                        {p.label || "(라벨 없음)"} — 발급 {p.createdAt.slice(0, 10)}
                      </li>
                    ))}
                  </ul>
                </details>
              )}
            </section>
          </>
        )}
      </main>
    </div>
  );
}
