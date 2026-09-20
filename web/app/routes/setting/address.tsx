import type { Route } from "./+types/address";
import { apiFetch, type Address } from "~/lib/api.server";
import { useT } from "~/lib/i18n";
import { requireUser } from "~/lib/session.server";
import { AddressIcon, Badge, Banner, CopyButton, EmptyState, Panel } from "~/kit";
import { ShellContent, ShellHeader } from "~/shell/app-shell";

// The addresses that reach this account. Adding/removing is an admin action
// (it decides who owns a mailbox), so this view is read-only by design.

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await requireUser(request);
  const addressList = await apiFetch<Address[]>(user.idToken, "/api/me/address").then((r) => r ?? []);
  return { addressList };
};

export default function AddressPage({ loaderData }: Route.ComponentProps) {
  const { addressList } = loaderData;
  const t = useT();

  return (
    <>
      <ShellHeader title={t("setting.address")} />
      <ShellContent>
        <Banner tone="info">{t("setting.addressAdminHint")}</Banner>
        <Panel>
          {addressList.length === 0 ? (
            <EmptyState
              icon={<AddressIcon className="size-7" strokeWidth={1.5} />}
              title={t("setting.noAddress")}
            />
          ) : (
            <ul className="divide-y divide-line">
              {addressList.map((address) => {
                const full = `${address.localPart}@${address.domainName}`;
                const wildcard = address.localPart === "*";
                return (
                  <li key={address.id} className="flex items-center gap-3 px-4 py-3">
                    <span className="min-w-0 flex-1 truncate font-mono text-sm text-ink">{full}</span>
                    {wildcard && <Badge tone="warn">{t("setting.catchAll")}</Badge>}
                    <CopyButton value={full} />
                  </li>
                );
              })}
            </ul>
          )}
        </Panel>
      </ShellContent>
    </>
  );
}
