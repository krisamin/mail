import { Form, Link, redirect, useNavigation } from "react-router";
import type { Route } from "./+types/account";
import { ApiError, apiFetch, type Address, type AppPassword, type Account } from "~/lib/api.server";
import { getUser, isAdmin } from "~/lib/session.server";
import {
  AddressChipList,
  AppPasswordRows,
  Button,
  Card,
  EmptyText,
  ErrorBanner,
  SecretReveal,
  TextInput,
} from "~/components";

// Self-service — the signed-in user's own mail account and app passwords.
// No admin group required; the Go API maps the token's sub claim to the account.
// Accounts appear via JIT provisioning on first login; address changes are admin-only.

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
      noAccount = true; // not provisioned yet (shouldn't happen after a normal login)
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
  const { name, email, admin, appPasswordList, addressList, noAccount } = loaderData;
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
          <Card className="p-6 text-center">
            <p className="text-sm text-text-1">
              <span className="font-mono">{email}</span> 에 연결된 메일 계정이 아직 없어요.
            </p>
            <p className="mt-1 text-xs text-text-2">다시 로그인하면 자동으로 만들어져요.</p>
          </Card>
        ) : (
          <>
            <Card className="p-4">
              <h1 className="text-lg font-bold">내 메일 계정</h1>
              <p className="mt-1 font-mono text-sm text-text-1">{email}</p>
              {addressList.length > 0 && (
                <div className="mt-2">
                  <p className="text-xs text-text-2">내 메일 주소 — 이 주소들로 받고 보낼 수 있어요:</p>
                  <div className="mt-1">
                    <AddressChipList list={addressList} />
                  </div>
                  <p className="mt-1.5 text-xs text-text-2">주소 추가는 관리자에게 요청해 주세요.</p>
                </div>
              )}
              <p className="mt-2 text-xs text-text-2">
                IMAP/SMTP 접속에는 아래에서 발급한 앱 비밀번호를 사용해요 (OIDC 비밀번호 아님).
              </p>
            </Card>

            <ErrorBanner message={actionData && !actionData.ok ? actionData.error : null} />

            {actionData?.ok && "plaintext" in actionData && actionData.plaintext && (
              <SecretReveal
                title="새 앱 비밀번호 — 지금만 표시돼요. 메일 앱에 바로 붙여넣으세요."
                value={actionData.plaintext}
              />
            )}

            <section className="flex flex-col gap-3">
              <h2 className="text-sm font-medium text-text-1">앱 비밀번호</h2>
              <Form method="post" className="flex gap-2">
                <input type="hidden" name="intent" value="create-pw" />
                <TextInput
                  name="label"
                  required
                  placeholder="라벨 (예: Thunderbird 노트북)"
                  className="flex-1"
                />
                <Button disabled={busy}>발급</Button>
              </Form>

              {active.length === 0 ? (
                <Card>
                  <EmptyText>활성 앱 비밀번호가 없어요. 위에서 발급해 주세요.</EmptyText>
                </Card>
              ) : (
                <Card className="px-4">
                  <AppPasswordRows list={active} busy={busy} />
                </Card>
              )}

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
