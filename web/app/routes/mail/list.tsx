import { useEffect } from "react";
import { Link, useRevalidator, useSearchParams } from "react-router";
import type { Route } from "./+types/list";
import { apiFetch, type MessagePage } from "~/lib/api.server";
import { useT } from "~/lib/i18n";
import { requireUser } from "~/lib/session.server";
import { Card, EmptyText, PageTitle, TimeText, Banner } from "~/components";
import { folderLabel } from "./layout";

// Message list — newest first, UID-cursor pagination ("load more" appends
// pages via the ?before= param so browser back keeps the position).

export const loader = async ({ request, params }: Route.LoaderArgs) => {
  const user = await requireUser(request);
  const mailbox = params.mailbox ?? "INBOX";
  const url = new URL(request.url);
  const before = url.searchParams.get("before") ?? "0";

  // accumulate pages up to the cursor: page size 50, at most the current
  // page — earlier rows stay because the loader re-fetches from the top
  // until the cursor. Simple + stateless (no client cache to invalidate).
  const pageList: MessagePage[] = [];
  let cursor = "0";
  for (let i = 0; i < 20; i++) {
    const page = await apiFetch<MessagePage>(
      user.idToken,
      `/api/me/message?mailbox=${encodeURIComponent(mailbox)}&limit=50&before=${cursor}`,
    );
    pageList.push(page);
    if (cursor === before || page.nextBefore === 0) break;
    cursor = String(page.nextBefore);
  }
  const messageList = pageList.flatMap((p) => p.messageList);
  const nextBefore = pageList[pageList.length - 1]?.nextBefore ?? 0;
  const sentNotice = url.searchParams.get("sent");
  return { mailbox, messageList, nextBefore, sentNotice };
};

export default function MessageList({ loaderData }: Route.ComponentProps) {
  const { mailbox, messageList, nextBefore, sentNotice } = loaderData;
  const t = useT();
  const [, setSearchParams] = useSearchParams();
  const revalidator = useRevalidator();

  // new mail appears without a manual refresh — poll while visible
  useEffect(() => {
    const id = setInterval(() => {
      if (document.visibilityState === "visible" && revalidator.state === "idle") {
        revalidator.revalidate();
      }
    }, 15_000);
    return () => clearInterval(id);
  }, [revalidator]);

  return (
    <div className="flex flex-col gap-4">
      <PageTitle title={folderLabel(t, mailbox)} />
      {sentNotice && <Banner title={sentNotice} />}
      <Card>
        {messageList.length === 0 ? (
          <EmptyText>{t("webmail.empty")}</EmptyText>
        ) : (
          <ul className="divide-y divide-line">
            {messageList.map((m) => (
              <li key={m.id}>
                <Link
                  to={`/mail/${encodeURIComponent(mailbox)}/${m.id}`}
                  className="flex items-center gap-3 px-4 py-2.5 hover:bg-bg-2"
                >
                  <span
                    aria-hidden
                    className={`h-2 w-2 shrink-0 rounded-full ${m.seen ? "bg-transparent" : "bg-accent"}`}
                  />
                  <div className="min-w-0 flex-1">
                    <div className="flex items-baseline justify-between gap-3">
                      <p
                        className={`truncate text-sm ${m.seen ? "text-text-2" : "font-medium text-text-0"}`}
                      >
                        {m.flagged && <span className="mr-1 text-warn">★</span>}
                        {m.subject || t("webmail.noSubject")}
                      </p>
                      <TimeText
                        value={m.internalDate}
                        className="shrink-0 text-xs text-muted"
                      />
                    </div>
                    <p className="truncate text-xs text-text-2">{m.fromAddr}</p>
                  </div>
                </Link>
              </li>
            ))}
          </ul>
        )}
      </Card>
      {nextBefore > 0 && (
        <button
          type="button"
          onClick={() => setSearchParams({ before: String(nextBefore) })}
          className="rounded-md border border-line px-4 py-2 text-center text-sm text-text-1 hover:bg-bg-2"
        >
          {t("webmail.loadMore")}
        </button>
      )}
    </div>
  );
}
