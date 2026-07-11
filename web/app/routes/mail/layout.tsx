import { Link, NavLink, Outlet, useParams } from "react-router";
import type { Route } from "./+types/layout";
import { apiFetch, type MailboxSummary } from "~/lib/api.server";
import { useT, type TFunc } from "~/lib/i18n";
import { requireUser } from "~/lib/session.server";
import { ButtonLink } from "~/components";

// Webmail shell — mailbox sidebar + content outlet.
// Any signed-in user (self-service surface, no admin group).

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await requireUser(request);
  const mailboxList = await apiFetch<MailboxSummary[]>(user.idToken, "/api/me/mailbox");
  return { name: user.name, email: user.email, mailboxList: mailboxList ?? [] };
};

/** Well-known folders get localized labels; custom ones show as-is. */
export const folderLabel = (t: TFunc, name: string): string => {
  switch (name) {
    case "INBOX":
      return t("webmail.folderINBOX");
    case "Sent":
      return t("webmail.folderSent");
    case "Trash":
      return t("webmail.folderTrash");
    case "Junk":
      return t("webmail.folderJunk");
    case "Archive":
      return t("webmail.folderArchive");
    case "Drafts":
      return t("webmail.folderDrafts");
    default:
      return name;
  }
};

export default function WebmailLayout({ loaderData }: Route.ComponentProps) {
  const { name, mailboxList } = loaderData;
  const t = useT();
  const params = useParams();
  const currentMailbox = params.mailbox ?? "INBOX";

  return (
    <div className="min-h-dvh">
      <header className="border-b border-line bg-bg-1">
        <div className="mx-auto flex max-w-6xl items-center justify-between px-4 py-3">
          <div className="flex items-center gap-6">
            <Link to="/" className="text-sm font-bold tracking-tight">
              mail <span className="text-accent">box</span>
            </Link>
          </div>
          <div className="flex items-center gap-3">
            <Link to="/account" className="text-xs text-text-2 hover:text-text-1">
              {t("nav.myAccount")}
            </Link>
            <span className="text-xs text-text-2">{name}</span>
            <Link to="/logout" className="text-xs text-text-2 hover:text-text-1">
              {t("common.logout")}
            </Link>
          </div>
        </div>
      </header>

      <div className="mx-auto flex max-w-6xl gap-6 px-4 py-6">
        <aside className="flex w-44 shrink-0 flex-col gap-3">
          <ButtonLink to="/mail/compose" className="w-full">
            {t("webmail.compose")}
          </ButtonLink>
          <nav className="flex flex-col gap-0.5">
            {mailboxList.map((m) => (
              <NavLink
                key={m.name}
                to={`/mail/${encodeURIComponent(m.name)}`}
                className={() =>
                  `flex items-center justify-between rounded-md px-3 py-1.5 text-sm transition-colors duration-100 ${
                    currentMailbox === m.name
                      ? "bg-bg-3 text-text-0"
                      : "text-text-2 hover:bg-bg-2 hover:text-text-1"
                  }`
                }
              >
                <span className="truncate">{folderLabel(t, m.name)}</span>
                {m.unseenCount > 0 && (
                  <span className="ml-2 rounded-full bg-accent/20 px-1.5 text-xs text-accent">
                    {m.unseenCount}
                  </span>
                )}
              </NavLink>
            ))}
          </nav>
        </aside>
        <main className="min-w-0 flex-1">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
