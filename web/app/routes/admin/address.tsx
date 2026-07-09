import { Form, Link, useNavigation } from "react-router";
import type { Route } from "./+types/address";
import {
  ApiError,
  apiFetch,
  type Address,
  type Domain,
  type Account,
} from "~/lib/api.server";
import { getUser } from "~/lib/session.server";

// 도메인별 주소 관리 — 주소를 계정에 연결한다 (admin 전용).
// 계정 자체는 JIT 프로비저닝(첫 로그인)으로 생긴다.

export const loader = async ({ request, params }: Route.LoaderArgs) => {
  const user = (await getUser(request))!;
  const domainId = params.domainId;

  const [domainList, addressList, accountList] = await Promise.all([
    apiFetch<Domain[]>(user.idToken, "/api/admin/domain"),
    apiFetch<Address[]>(user.idToken, `/api/admin/domain/${domainId}/address`),
    apiFetch<Account[]>(user.idToken, "/api/admin/account"),
  ]);
  const domain = (domainList ?? []).find((d) => String(d.id) === domainId);
  if (!domain) throw new Response("도메인을 찾을 수 없어요", { status: 404 });

  return { domain, addressList: addressList ?? [], accountList: accountList ?? [] };
};

export const action = async ({ request, params }: Route.ActionArgs) => {
  const user = (await getUser(request))!;
  const form = await request.formData();
  const intent = form.get("intent");

  try {
    switch (intent) {
      case "create-address": {
        await apiFetch(user.idToken, `/api/admin/domain/${params.domainId}/address`, {
          method: "POST",
          body: {
            localPart: String(form.get("localPart") ?? ""),
            accountId: Number(form.get("accountId")),
          },
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

export default function DomainAddressList({ loaderData, actionData }: Route.ComponentProps) {
  const { domain, addressList, accountList } = loaderData;
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

      <section className="flex flex-col gap-3">
        <div>
          <h2 className="text-sm font-medium text-text-1">주소</h2>
          <p className="mt-0.5 text-xs text-text-2">
            이 도메인의 메일 주소를 계정에 연결해요. local part에{" "}
            <code className="rounded bg-bg-3 px-1">*</code>를 넣으면 catch-all (이 도메인의 모든
            미지정 주소). 계정은 유저가 처음 로그인할 때 자동으로 생겨요.
          </p>
        </div>

        <Form method="post" className="flex gap-2">
          <input type="hidden" name="intent" value="create-address" />
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
                  {u.email}
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
          {addressList.length === 0 ? (
            <p className="px-4 py-4 text-center text-xs text-text-2">주소 없음</p>
          ) : (
            <ul className="divide-y divide-line">
              {addressList.map((a) => (
                <li key={a.id} className="flex items-center justify-between px-4 py-2.5">
                  <div className="flex items-center gap-2">
                    <span className="font-mono text-sm text-text-0">
                      {a.localPart === "*" ? <span className="text-warn">*</span> : a.localPart}
                      <span className="text-text-2">@{a.domainName}</span>
                    </span>
                    {a.localPart === "*" && (
                      <span className="rounded bg-warn/15 px-1.5 py-0.5 text-[10px] text-warn">
                        catch-all
                      </span>
                    )}
                    <span className="text-xs text-text-2">→ {a.accountEmail}</span>
                  </div>
                  <Form method="post">
                    <input type="hidden" name="intent" value="delete-address" />
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
