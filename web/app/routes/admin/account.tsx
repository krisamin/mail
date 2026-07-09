import { Form, useNavigation } from "react-router";
import type { Route } from "./+types/account";
import {
  ApiError,
  apiFetch,
  type Address,
  type AppPassword,
  type Account,
  type Domain,
} from "~/lib/api.server";
import { getUser } from "~/lib/session.server";
import {
  ActiveToggle,
  AddressChipList,
  AppPasswordRows,
  Badge,
  Button,
  Card,
  EmptyText,
  ErrorBanner,
  PageTitle,
  SecretReveal,
  SelectInput,
  TextInput,
} from "~/components";

// Account management — every account with its addresses and app passwords.
// Human accounts appear via JIT provisioning (first OIDC login); service
// accounts (no login, address + app password only) are created here.

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = (await getUser(request))!;

  const [accountList, domainList] = await Promise.all([
    apiFetch<Account[]>(user.idToken, "/api/admin/account").then((r) => r ?? []),
    apiFetch<Domain[]>(user.idToken, "/api/admin/domain").then((r) => r ?? []),
  ]);

  // Per-account fan-out is fine here — admin page, small account count.
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
  return {
    accountList,
    domainList: domainList.filter((d) => d.active),
    addressList,
    appPasswordList,
  };
};

export const action = async ({ request }: Route.ActionArgs) => {
  const user = (await getUser(request))!;
  const form = await request.formData();
  const intent = form.get("intent");

  try {
    switch (intent) {
      case "create-service": {
        await apiFetch(user.idToken, "/api/admin/account/service", {
          method: "POST",
          body: {
            email: `${String(form.get("localPart") ?? "")}@${String(form.get("domainName") ?? "")}`,
          },
        });
        return { ok: true as const };
      }
      case "create-address": {
        await apiFetch(user.idToken, `/api/admin/account/${form.get("accountId")}/address`, {
          method: "POST",
          body: {
            localPart: String(form.get("localPart") ?? ""),
            domainId: Number(form.get("domainId")),
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
        return { ok: true as const, plaintext: result.plaintext };
      }
      case "revoke-pw": {
        await apiFetch(user.idToken, `/api/admin/app-password/${form.get("id")}`, {
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
  const { accountList, domainList, addressList, appPasswordList } = loaderData;
  const nav = useNavigation();
  const busy = nav.state !== "idle";

  return (
    <div className="flex flex-col gap-6">
      <PageTitle
        title="계정"
        description="사람 계정은 첫 로그인 때 자동 생성 (OIDC 신원 기준). 서비스 계정은 로그인 없이 주소·앱 비밀번호만 갖는 시스템용."
      />

      <ErrorBanner message={actionData && !actionData.ok ? actionData.error : null} />

      {actionData?.ok && "plaintext" in actionData && actionData.plaintext && (
        <SecretReveal title="앱 비밀번호 — 지금만 표시됨" value={actionData.plaintext} />
      )}

      {/* Service account creation */}
      <Form method="post" className="flex gap-2">
        <input type="hidden" name="intent" value="create-service" />
        <div className="flex flex-1 items-center gap-1 rounded-md border border-line bg-bg-1 px-3">
          <input
            name="localPart"
            required
            placeholder="bot"
            className="flex-1 bg-transparent py-2 text-sm outline-none"
          />
          <span className="text-sm text-text-2">@</span>
          <SelectInput name="domainName" required fieldSize="sm" className="py-1">
            {domainList.map((d) => (
              <option key={d.id} value={d.name}>
                {d.name}
              </option>
            ))}
          </SelectInput>
        </div>
        <Button disabled={busy || domainList.length === 0}>서비스 계정 추가</Button>
      </Form>

      <div className="flex flex-col gap-3">
        {accountList.length === 0 ? (
          <Card>
            <EmptyText>계정 없음 — 유저가 로그인하거나 서비스 계정을 만들면 여기 나타나요.</EmptyText>
          </Card>
        ) : (
          accountList.map((u) => (
            <Card key={u.id}>
              <div className="flex items-center justify-between border-b border-line px-4 py-2.5">
                <div className="flex items-center gap-2">
                  <div>
                    <p className="text-sm font-medium">{u.email}</p>
                    {u.kind === "user" && (
                      <p className="font-mono text-[10px] text-text-2">sub: {u.subject}</p>
                    )}
                  </div>
                  {u.kind === "service" && <Badge tone="accent">서비스</Badge>}
                </div>
                <Form method="post">
                  <input type="hidden" name="intent" value="toggle-account" />
                  <input type="hidden" name="id" value={u.id} />
                  <input type="hidden" name="active" value={String(!u.active)} />
                  <ActiveToggle active={u.active} disabled={busy} />
                </Form>
              </div>

              {/* Addresses: chips + inline [local]@[domain] add */}
              <div className="flex flex-col gap-2 px-4 py-3">
                <p className="text-xs text-text-2">주소</p>
                <AddressChipList list={addressList[u.id] ?? []} busy={busy} deletable />
                <Form method="post" className="flex items-center gap-1.5">
                  <input type="hidden" name="intent" value="create-address" />
                  <input type="hidden" name="accountId" value={u.id} />
                  <TextInput
                    name="localPart"
                    required
                    placeholder="hello 또는 *"
                    fieldSize="sm"
                    className="w-32"
                  />
                  <span className="text-xs text-text-2">@</span>
                  <SelectInput name="domainId" required fieldSize="sm">
                    {domainList.map((d) => (
                      <option key={d.id} value={d.id}>
                        {d.name}
                      </option>
                    ))}
                  </SelectInput>
                  <Button variant="link" disabled={busy}>
                    추가
                  </Button>
                </Form>
              </div>

              {/* App passwords */}
              <div className="flex flex-col gap-2 border-t border-line px-4 py-3">
                <div className="flex items-center justify-between">
                  <p className="text-xs text-text-2">앱 비밀번호</p>
                  <Form method="post" className="flex items-center gap-1.5">
                    <input type="hidden" name="intent" value="create-pw" />
                    <input type="hidden" name="accountId" value={u.id} />
                    <TextInput
                      name="label"
                      placeholder="라벨 (예: Thunderbird)"
                      fieldSize="sm"
                      className="w-40"
                    />
                    <Button variant="link" disabled={busy}>
                      발급
                    </Button>
                  </Form>
                </div>
                <AppPasswordRows list={appPasswordList[u.id] ?? []} busy={busy} />
              </div>
            </Card>
          ))
        )}
      </div>
    </div>
  );
}
