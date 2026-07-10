import { Form, useNavigation } from "react-router";
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
import { getLocale } from "~/lib/locale.server";
import { requireUser } from "~/lib/session.server";
import {
  ActiveToggle,
  Badge,
  Banner,
  Button,
  Card,
  EmptyText,
  ErrorBanner,
  PageTitle,
  SelectInput,
  TextInput,
} from "~/components";

// Domain management — create/toggle domains, DKIM keys, DNS verification.
// Address ↔ account wiring lives on /admin/account.

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await requireUser(request);
  const domainList = await apiFetch<Domain[]>(user.idToken, "/api/admin/domain");
  return { domainList: domainList ?? [] };
};

export const action = async ({ request }: Route.ActionArgs) => {
  const user = await requireUser(request);
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
  ] as const;

export default function DomainList({ loaderData, actionData }: Route.ComponentProps) {
  const { domainList } = loaderData;
  const t = useT();
  const nav = useNavigation();
  const busy = nav.state !== "idle";

  return (
    <div className="flex flex-col gap-6">
      <PageTitle title={t("domain.title")} description={t("domain.description")} />

      <ErrorBanner message={actionData && !actionData.ok ? actionData.error : null} />

      {actionData?.ok && "dkim" in actionData && actionData.dkim && (
        <Banner title={t("domain.dkimIssued")}>
          <p className="mt-2 font-mono text-xs text-text-1">{actionData.dkim.dnsName} IN TXT</p>
          <p className="mt-1 break-all rounded bg-bg-0 p-2 font-mono text-xs text-text-1">
            {actionData.dkim.dnsTxt}
          </p>
        </Banner>
      )}

      {actionData?.ok && "dns" in actionData && actionData.dns && (
        <Card className="p-4">
          <p className="mb-2 text-sm font-medium">
            {t("domain.dnsVerifyPrefix")} <span className="font-mono">{actionData.dns.domain}</span>
          </p>
          <ul className="flex flex-col gap-1.5">
            {dnsCheckList(actionData.dns).map(([label, check]) => (
              <li key={label} className="flex flex-col gap-0.5">
                <div className="flex items-center gap-2">
                  <Badge
                    tone={check.status === "ok" ? "ok" : check.status === "warn" ? "warn" : "bad"}
                    className="font-medium"
                  >
                    {check.status === "ok" ? "✓" : check.status === "warn" ? "!" : "✗"} {label}
                  </Badge>
                  {check.found && (
                    <span
                      className="max-w-lg truncate font-mono text-[10px] text-text-2"
                      title={check.found}
                    >
                      {check.found}
                    </span>
                  )}
                </div>
                {check.note && <p className="pl-1 text-[11px] text-text-2">{check.note}</p>}
                {check.expected && check.status !== "ok" && (
                  <p
                    className="break-all rounded bg-bg-0 p-1.5 pl-1 font-mono text-[10px] text-text-1"
                    title={t("domain.expectedValue")}
                  >
                    {check.expected}
                  </p>
                )}
              </li>
            ))}
          </ul>
        </Card>
      )}

      <Form method="post" className="flex gap-2">
        <input type="hidden" name="intent" value="create" />
        <TextInput name="name" required placeholder="example.com" className="flex-1" />
        <Button disabled={busy}>{t("common.add")}</Button>
      </Form>

      <Card>
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
                      <Button variant="chip" disabled={busy}>
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
                      <Badge tone="accent">DKIM: {d.dkimSelector}</Badge>
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
                        <Button variant="linkDanger" disabled={busy} className="text-[10px]">
                          {t("domain.dkimClear")}
                        </Button>
                      </Form>
                    </>
                  ) : (
                    <Form method="post" className="flex items-center gap-1.5">
                      <input type="hidden" name="intent" value="dkim" />
                      <input type="hidden" name="id" value={d.id} />
                      <TextInput name="selector" defaultValue="mail" fieldSize="sm" className="w-24" />
                      <SelectInput name="keyType" defaultValue="rsa2048" fieldSize="sm">
                        <option value="rsa2048">{t("domain.rsaCompat")}</option>
                        <option value="ed25519">Ed25519</option>
                      </SelectInput>
                      <Button variant="link" disabled={busy}>
                        {t("domain.dkimCreate")}
                      </Button>
                    </Form>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  );
}
