import { Outlet, useFetcher, useParams } from "react-router";
import type { Route } from "./+types/layout";
import { apiFetch, type MailboxSummary } from "~/lib/api.server";
import { useT, type TFunc } from "~/lib/i18n";
import { readPreference } from "~/lib/preference.server";
import { requireUser } from "~/lib/session.server";
import { themeFromCookie } from "~/lib/preference.server";
import { AppShell } from "~/shell/app-shell";
import { SideNav, SideNavItem } from "~/shell/side-nav";
import {
  ArchiveIcon,
  Button,
  ComposeIcon,
  CountBadge,
  DraftIcon,
  FolderIcon,
  InboxIcon,
  JunkIcon,
  PlusIcon,
  SendIcon,
  TrashIcon,
} from "~/kit";
import { isAdmin } from "~/lib/session.server";
import { useState } from "react";

// Mail area: folder column in the shell sidebar, list + reading pane inside.

// Well-known folders pin to the top in mail-client order; custom folders
// follow alphabetically (the API sorts INBOX-first/alphabetical, which drops
// Sent between Junk and Trash — jarring next to every other client).
const folderOrderMap: Record<string, number> = {
  INBOX: 0,
  Drafts: 1,
  Sent: 2,
  Archive: 3,
  Junk: 4,
  Trash: 5,
};

const folderIconMap: Record<string, typeof InboxIcon> = {
  INBOX: InboxIcon,
  Drafts: DraftIcon,
  Sent: SendIcon,
  Archive: ArchiveIcon,
  Junk: JunkIcon,
  Trash: TrashIcon,
};

/** Well-known folders get localized labels; custom ones show as-is. */
export const folderLabel = (t: TFunc, name: string): string => {
  switch (name) {
    case "INBOX":
      return t("folder.inbox");
    case "Sent":
      return t("folder.sent");
    case "Drafts":
      return t("folder.draft");
    case "Trash":
      return t("folder.trash");
    case "Junk":
      return t("folder.junk");
    case "Archive":
      return t("folder.archive");
    default:
      return name;
  }
};

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await requireUser(request);
  const [mailboxList, preference] = await Promise.all([
    apiFetch<MailboxSummary[]>(user.idToken, "/api/me/mailbox").then((r) => r ?? []),
    readPreference(user),
  ]);
  mailboxList.sort((a, b) => {
    const oa = folderOrderMap[a.name] ?? 100;
    const ob = folderOrderMap[b.name] ?? 100;
    return oa !== ob ? oa - ob : a.name.localeCompare(b.name);
  });
  return {
    mailboxList,
    user: {
      name: user.name,
      email: user.email,
      admin: isAdmin(user),
      theme: preference.theme === "system" ? themeFromCookie(request) : preference.theme,
    },
  };
};

export default function MailLayout({ loaderData }: Route.ComponentProps) {
  const { mailboxList, user } = loaderData;
  const t = useT();
  const params = useParams();
  const fetcher = useFetcher();
  const [adding, setAdding] = useState(false);
  const current = params.mailbox ?? "INBOX";

  const sidebar = (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="p-2">
        <fetcher.Form method="get" action={`/mail/${encodeURIComponent(current)}/compose`}>
          <Button type="submit" className="w-full">
            <ComposeIcon className="size-4" />
            {t("mail.compose")}
          </Button>
        </fetcher.Form>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto scroll-thin">
        <SideNav>
          {mailboxList.map((box) => {
            const Icon = folderIconMap[box.name] ?? FolderIcon;
            return (
              <SideNavItem
                key={box.name}
                to={`/mail/${encodeURIComponent(box.name)}`}
                icon={<Icon className="size-4" />}
                label={folderLabel(t, box.name)}
                trailing={<CountBadge value={box.unseenCount} />}
              />
            );
          })}
        </SideNav>
      </div>
      <div className="border-t border-line p-2">
        {adding ? (
          <fetcher.Form
            method="post"
            action="/mail/folder"
            className="flex gap-1.5"
            onSubmit={() => setAdding(false)}
          >
            <input
              name="name"
              autoFocus
              placeholder={t("mail.folderName")}
              className="h-8 w-full min-w-0 rounded-md border border-line bg-canvas px-2 text-xs text-ink placeholder:text-ink-faint focus:border-brand focus:outline-none"
              onBlur={(e) => {
                if (!e.currentTarget.value) setAdding(false);
              }}
            />
          </fetcher.Form>
        ) : (
          <Button variant="ghost" size="sm" className="w-full justify-start" onClick={() => setAdding(true)} type="button">
            <PlusIcon className="size-4" />
            {t("mail.newFolder")}
          </Button>
        )}
      </div>
    </div>
  );

  return (
    <AppShell user={user} sidebar={sidebar}>
      <Outlet />
    </AppShell>
  );
}
