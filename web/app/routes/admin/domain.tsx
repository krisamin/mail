import { Form, useNavigation } from "react-router";
import type { Route } from "./+types/domain";
import {
  ApiError,
  apiFetch,
  type DKIMResult,
  type DnsVerify,
  type Domain,
} from "~/lib/api.server";
import { getUser } from "~/lib/session.server";

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = (await getUser(request))!;
  const domainList = await apiFetch<Domain[]>(user.idToken, "/api/admin/domain");
  return { domainList: domainList ?? [] };
};

export const action = async ({ request }: Route.ActionArgs) => {
  const user = (await getUser(request))!;
  const form = await request.formData();
  const intent = form.get("intent");

  try {
    switch (intent) {
      case "create": {
        await apiFetch(user.idToken, "/api/admin/domain", {
          method: "POST",
          body: { name: String(form.get("name") ?? "") },
        });
        return { ok: true as const };
      }
      case "toggle": {
        await apiFetch(user.idToken, `/api/admin/domain/${form.get("id")}`, {
          method: "PATCH",
          body: { active: form.get("active") === "true" },
        });
        return { ok: true as const };
      }
      case "dkim": {
        const result = await apiFetch<DKIMResult>(
          user.idToken,
          `/api/admin/domain/${form.get("id")}/dkim`,
          {
            method: "POST",
            body: {
              selector: String(form.get("selector") ?? "mail"),
              keyType: String(form.get("keyType") ?? "rsa2048"),
            },
          },
        );
        return { ok: true as const, dkim: result };
      }
      case "dkim-clear": {
        await apiFetch(user.idToken, `/api/admin/domain/${form.get("id")}/dkim`, {
          method: "DELETE",
        });
        return { ok: true as const };
      }
      case "dns-verify": {
        const dns = await apiFetch<DnsVerify>(
          user.idToken,
          `/api/admin/domain/${form.get("id")}/dns`,
        );
        return { ok: true as const, dns };
      }
      default:
        return { ok: false as const, error: "알 수 없는 요청" };
    }
  } catch (e) {
    if (e instanceof ApiError) return { ok: false as const, error: e.message };
    throw e;
  }
};

export default function DomainList({ loaderData, actionData }: Route.ComponentProps) {
  const { domainList } = loaderData;
  const nav = useNavigation();
  const busy = nav.state !== "idle";

  return (
    <div className="flex flex-col gap-6">
      <h1 className="text-xl font-bold">도메인</h1>

      {actionData && !actionData.ok && (
        <p className="rounded-md border border-bad/40 bg-bad/10 px-3 py-2 text-sm text-bad">
          {actionData.error}
        </p>
      )}

      {actionData?.ok && "dkim" in actionData && actionData.dkim && (
        <div className="rounded-md border border-accent/40 bg-accent-soft p-4">
          <p className="text-sm font-medium text-accent">DKIM 키 생성됨 — DNS TXT 레코드 등록:</p>
          <p className="mt-2 font-mono text-xs text-text-1">
            {actionData.dkim.dnsName} IN TXT
          </p>
          <p className="mt-1 break-all rounded bg-bg-0 p-2 font-mono text-xs text-text-1">
            {actionData.dkim.dnsTxt}
          </p>
        </div>
      )}

      {actionData?.ok && "dns" in actionData && actionData.dns && (
        <div className="rounded-md border border-line bg-bg-1 p-4">
          <p className="mb-2 text-sm font-medium">
            DNS 검증 — <span className="font-mono">{actionData.dns.domain}</span>
          </p>
          <ul className="flex flex-col gap-1.5">
            {(
              [
                ["MX", actionData.dns.mx],
                ["SPF", actionData.dns.spf],
                ["DKIM", actionData.dns.dkim],
                ["DMARC", actionData.dns.dmarc],
              ] as const
            ).map(([label, check]) => (
              <li key={label} className="flex flex-col gap-0.5">
                <div className="flex items-center gap-2">
                  <span
                    className={`rounded px-1.5 py-0.5 text-[10px] font-medium ${
                      check.status === "ok"
                        ? "bg-ok/20 text-ok"
                        : check.status === "warn"
                          ? "bg-warn/20 text-warn"
                          : "bg-bad/20 text-bad"
                    }`}
                  >
                    {check.status === "ok" ? "✓" : check.status === "warn" ? "!" : "✗"} {label}
                  </span>
                  {check.found && (
                    <span className="max-w-lg truncate font-mono text-[10px] text-text-2" title={check.found}>
                      {check.found}
                    </span>
                  )}
                </div>
                {check.note && <p className="pl-1 text-[11px] text-text-2">{check.note}</p>}
                {check.expected && check.status !== "ok" && (
                  <p
                    className="break-all rounded bg-bg-0 p-1.5 pl-1 font-mono text-[10px] text-text-1"
                    title="등록할 값"
                  >
                    {check.expected}
                  </p>
                )}
              </li>
            ))}
          </ul>
        </div>
      )}

      <Form method="post" className="flex gap-2">
        <input type="hidden" name="intent" value="create" />
        <input
          name="name"
          required
          placeholder="example.com"
          className="flex-1 rounded-md border border-line bg-bg-1 px-3 py-2 text-sm outline-none focus:border-accent"
        />
        <button
          type="submit"
          disabled={busy}
          className="rounded-md bg-accent px-4 py-2 text-sm font-medium text-bg-0 hover:bg-accent-hover disabled:opacity-50"
        >
          추가
        </button>
      </Form>

      <div className="rounded-md border border-line bg-bg-1">
        {domainList.length === 0 ? (
          <p className="px-4 py-6 text-center text-sm text-text-2">도메인 없음</p>
        ) : (
          <ul className="divide-y divide-line">
            {domainList.map((d) => (
              <li key={d.id} className="flex flex-col gap-2 px-4 py-3">
                <div className="flex items-center justify-between">
                  <a href={`/admin/domain/${d.id}/account`} className="text-sm font-medium hover:text-accent">
                    {d.name}
                  </a>
                  <div className="flex items-center gap-2">
                    <Form method="post">
                      <input type="hidden" name="intent" value="dns-verify" />
                      <input type="hidden" name="id" value={d.id} />
                      <button
                        type="submit"
                        disabled={busy}
                        className="rounded px-2 py-1 text-xs bg-bg-3 text-text-2 hover:bg-bg-2"
                      >
                        DNS 검증
                      </button>
                    </Form>
                    <Form method="post">
                      <input type="hidden" name="intent" value="toggle" />
                      <input type="hidden" name="id" value={d.id} />
                      <input type="hidden" name="active" value={String(!d.active)} />
                      <button
                        type="submit"
                        disabled={busy}
                        className={`rounded px-2 py-1 text-xs ${
                          d.active
                            ? "bg-ok/20 text-ok hover:bg-ok/30"
                            : "bg-bg-3 text-muted hover:bg-bg-2"
                        }`}
                      >
                        {d.active ? "활성" : "비활성"}
                      </button>
                    </Form>
                  </div>
                </div>

                <div className="flex items-center gap-2">
                  {d.dkimSelector ? (
                    <>
                      <span className="rounded bg-accent-soft px-1.5 py-0.5 text-[10px] text-accent">
                        DKIM: {d.dkimSelector}
                      </span>
                      {d.dkimPublicTxt && (
                        <span
                          className="max-w-md truncate font-mono text-[10px] text-text-2"
                          title={d.dkimPublicTxt}
                        >
                          {d.dkimPublicTxt}
                        </span>
                      )}
                      <Form method="post">
                        <input type="hidden" name="intent" value="dkim-clear" />
                        <input type="hidden" name="id" value={d.id} />
                        <button type="submit" disabled={busy} className="text-[10px] text-bad hover:underline">
                          해제
                        </button>
                      </Form>
                    </>
                  ) : (
                    <Form method="post" className="flex items-center gap-1.5">
                      <input type="hidden" name="intent" value="dkim" />
                      <input type="hidden" name="id" value={d.id} />
                      <input
                        name="selector"
                        defaultValue="mail"
                        className="w-24 rounded border border-line bg-bg-0 px-2 py-0.5 text-xs outline-none focus:border-accent"
                      />
                      <select
                        name="keyType"
                        defaultValue="rsa2048"
                        className="rounded border border-line bg-bg-0 px-1.5 py-0.5 text-xs outline-none focus:border-accent"
                      >
                        <option value="rsa2048">RSA-2048 (호환 ◎)</option>
                        <option value="ed25519">Ed25519</option>
                      </select>
                      <button type="submit" disabled={busy} className="text-xs text-accent hover:underline">
                        DKIM 키 생성
                      </button>
                    </Form>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
