import { useEffect, useRef } from "react";
import { Form, useFetcher, useNavigation } from "react-router";
import type { Route } from "./+types/domain";
import {
  ApiError,
  apiFetch,
  type DKIMResult,
  type DnsVerify,
  type Domain,
} from "~/lib/api.server";
import { translate } from "~/i18n";
import { useT } from "~/lib/i18n";
import { ShellContent, ShellHeader } from "~/shell/app-shell";
import { getLocale } from "~/lib/locale.server";
import { requireAdmin } from "~/lib/session.server";
import {
  ActiveToggle,
  Badge,
  Banner,
  Button,
  Panel,
  CopyButton,
  EmptyText,
  ErrorBanner,
  SelectInput,
  Switch,
  TextInput,
} from "~/kit";

// Domain management — create/toggle domains, DKIM keys, DNS verification.
// Address ↔ account wiring lives on /admin/account.

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await requireAdmin(request);
  const domainList = await apiFetch<Domain[]>(user.idToken, "/api/admin/domain");
  return { domainList: domainList ?? [] };
};

export const action = async ({ request }: Route.ActionArgs) => {
  const user = await requireAdmin(request);
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
      case "permission": {
        await apiFetch(user.idToken, `/api/admin/domain/${form.get("id")}/permission`, {
          method: "PUT",
          body: {
            allowSendExternal: form.get("allowSendExternal") === "on",
            allowReceiveExternal: form.get("allowReceiveExternal") === "on",
          },
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
          { timeoutMs: 20_000 }, // eight sequential lookups against 1.1.1.1
        );
        return { ok: true as const, dns };
      }
      default:
        return {
          ok: false as const,
          error: translate(await getLocale(request), "common.unknownIntent"),
        };
    }
  } catch (e) {
    if (e instanceof ApiError) return { ok: false as const, error: e.message };
    throw e;
  }
};

const dnsCheckList = (dns: DnsVerify) =>
  [
    ["MX", dns.mx],
    ["SPF", dns.spf],
    ["DKIM", dns.dkim],
    ["DMARC", dns.dmarc],
    ["SRV imaps (993)", dns.srvImaps],
    ["SRV submissions (465)", dns.srvSubmissions],
    ["SRV submission (587)", dns.srvSubmission],
    ["autoconfig", dns.autoconfig],
  ] as const;

const dnsBadge = (status: string): { tone: "ok" | "warn" | "bad" | "muted"; mark: string } => {
  switch (status) {
    case "ok":
      return { tone: "ok", mark: "✓" };
    case "warn":
      return { tone: "warn", mark: "!" };
    case "error":
      return { tone: "muted", mark: "?" }; // lookup failed ≠ record missing
    default:
      return { tone: "bad", mark: "✗" };
  }
};

// DNS check results, rendered inline inside the owning domain row.
function DnsResultPanel({ dns }: { dns: DnsVerify }) {
  const t = useT();
  return (
    <ul className="mt-1 flex flex-col gap-1.5 rounded-md bg-canvas/50 p-2.5">
      {dnsCheckList(dns).map(([label, check]) => {
        const badge = dnsBadge(check.status);
        return (
          <li key={label} className="flex flex-col gap-0.5">
            <div className="flex items-center gap-2">
              <Badge tone={badge.tone} className="font-medium">
                {badge.mark} {label}
              </Badge>
              {check.found && (
                <span
                  className="max-w-lg truncate font-mono text-[10px] text-ink-3"
                  title={check.found}
                >
                  {check.found}
                </span>
              )}
            </div>
            {check.note && <p className="pl-1 text-[11px] text-ink-3">{check.note}</p>}
            {check.expected && check.status !== "ok" && (
              <div className="flex items-start gap-1.5">
                <p
                  className="flex-1 break-all rounded bg-canvas p-1.5 pl-1 font-mono text-[10px] text-ink-2"
                  title={t("domain.expectedValue")}
                >
                  {check.expected}
                </p>
                <CopyButton value={check.expected} />
              </div>
            )}
          </li>
        );
      })}
    </ul>
  );
}


/** The domain-wide kill switches. Each flip saves on its own. */
const DomainPermission = ({ domain }: { domain: Domain }) => {
  const t = useT();
  const fetcher = useFetcher();
  const busy = fetcher.state !== "idle";
  const pending = fetcher.formData;
  const read = (field: string, fallback: boolean) =>
    pending ? pending.get(field) === "on" : fallback;
  const send = read("allowSendExternal", domain.allowSendExternal);
  const receive = read("allowReceiveExternal", domain.allowReceiveExternal);

  const save = (next: { allowSendExternal?: boolean; allowReceiveExternal?: boolean }) =>
    fetcher.submit(
      {
        intent: "permission",
        id: domain.id,
        allowSendExternal: (next.allowSendExternal ?? send) ? "on" : "",
        allowReceiveExternal: (next.allowReceiveExternal ?? receive) ? "on" : "",
      },
      { method: "post" },
    );

  return (
    <div className="rounded-md bg-raised/40 px-3 py-2">
      <p className="text-xs text-ink-3">{t("permission.domainTitle")}</p>
      <div className="grid gap-x-6 sm:grid-cols-2">
        <Switch
          checked={send}
          disabled={busy}
          label={t("permission.domainSendExternal")}
          onChange={(v) => save({ allowSendExternal: v })}
        />
        <Switch
          checked={receive}
          disabled={busy}
          label={t("permission.domainReceiveExternal")}
          onChange={(v) => save({ allowReceiveExternal: v })}
        />
      </div>
      <p className="text-xs text-ink-faint">{t("permission.domainHint")}</p>
    </div>
  );
};

export default function DomainList({ loaderData, actionData }: Route.ComponentProps) {
  const { domainList } = loaderData;
  const t = useT();
  const nav = useNavigation();
  const busy = nav.state !== "idle";
  // Scope pending feedback to the submitting intent (not every button on the page).
  const pendingIntent = busy ? nav.formData?.get("intent") : null;
  const createFormRef = useRef<HTMLFormElement>(null);

  // Clear the create field after a successful submit (ready for the next add).
  useEffect(() => {
    if (actionData?.ok) createFormRef.current?.reset();
  }, [actionData]);

  return (
    <>
      <ShellHeader title={t("domain.title")} />
      <ShellContent>

      <ErrorBanner message={actionData && !actionData.ok ? actionData.error : null} />

      {actionData?.ok && "dkim" in actionData && actionData.dkim && (
        <Banner>
            {t("domain.dkimIssued")}
          <div className="mt-2 flex items-center justify-between">
            <p className="font-mono text-xs text-ink-2">{actionData.dkim.dnsName} IN TXT</p>
            <CopyButton value={actionData.dkim.dnsTxt} />
          </div>
          <p className="mt-1 break-all rounded bg-canvas p-2 font-mono text-xs text-ink-2">
            {actionData.dkim.dnsTxt}
          </p>
        </Banner>
      )}

      <Form method="post" ref={createFormRef} className="flex gap-2">
        <input type="hidden" name="intent" value="create" />
        <TextInput
          name="name"
          required
          placeholder="example.com"
          aria-label={t("domain.title")}
          className="flex-1"
        />
        <Button disabled={busy} pending={pendingIntent === "create"}>
          {t("common.add")}
        </Button>
      </Form>

      <Panel>
        {domainList.length === 0 ? (
          <EmptyText>{t("domain.none")}</EmptyText>
        ) : (
          <ul className="divide-y divide-line">
            {domainList.map((d) => (
              <li key={d.id} className="flex flex-col gap-2 px-4 py-3">
                <div className="flex items-center justify-between">
                  <span className="text-sm font-medium">{d.name}</span>
                  <div className="flex items-center gap-2">
                    <Form method="post">
                      <input type="hidden" name="intent" value="dns-verify" />
                      <input type="hidden" name="id" value={d.id} />
                      <Button
                        variant="subtle" size="sm"
                        disabled={busy}
                        pending={
                          pendingIntent === "dns-verify" &&
                          nav.formData?.get("id") === String(d.id)
                        }
                      >
                        {t("domain.dnsVerify")}
                      </Button>
                    </Form>
                    <Form method="post">
                      <input type="hidden" name="intent" value="toggle" />
                      <input type="hidden" name="id" value={d.id} />
                      <input type="hidden" name="active" value={String(!d.active)} />
                      <ActiveToggle active={d.active} disabled={busy} />
                    </Form>
                  </div>
                </div>

                <div className="flex items-center gap-2">
                  {d.dkimSelector ? (
                    <>
                      <Badge tone="brand">DKIM: {d.dkimSelector}</Badge>
                      {d.dkimPublicTxt && (
                        <>
                          <span
                            className="max-w-md truncate font-mono text-[10px] text-ink-3"
                            title={d.dkimPublicTxt}
                          >
                            {d.dkimPublicTxt}
                          </span>
                          <CopyButton value={d.dkimPublicTxt} />
                        </>
                      )}
                      <Form method="post">
                        <input type="hidden" name="intent" value="dkim-clear" />
                        <input type="hidden" name="id" value={d.id} />
                        <Button
                          variant="danger" size="sm"
                          disabled={busy}
                          confirmMessage={t("common.confirmDkimClear")}
                          className="text-[10px]"
                        >
                          {t("domain.dkimClear")}
                        </Button>
                      </Form>
                    </>
                  ) : (
                    <Form method="post" className="flex items-center gap-1.5">
                      <input type="hidden" name="intent" value="dkim" />
                      <input type="hidden" name="id" value={d.id} />
                      <TextInput
                        name="selector"
                        defaultValue="mail"
                        fieldSize="sm"
                        aria-label="DKIM selector"
                        className="w-24"
                      />
                      <SelectInput name="keyType" defaultValue="rsa2048" fieldSize="sm">
                        <option value="rsa2048">{t("domain.rsaCompat")}</option>
                        <option value="ed25519">Ed25519</option>
                      </SelectInput>
                      <Button variant="ghost" size="sm" disabled={busy} pending={pendingIntent === "dkim"}>
                        {t("domain.dkimCreate")}
                      </Button>
                    </Form>
                  )}
                </div>

                <DomainPermission domain={d} />

                {actionData?.ok &&
                  "dns" in actionData &&
                  actionData.dns &&
                  actionData.dns.domain === d.name && <DnsResultPanel dns={actionData.dns} />}
              </li>
            ))}
          </ul>
        )}
      </Panel>
      </ShellContent>
    </>
  );
}
