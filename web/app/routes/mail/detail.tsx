import { Form, Link, redirect, useNavigation } from "react-router";
import type { Route } from "./+types/detail";
import { ApiError, apiFetch, type MessageDetail } from "~/lib/api.server";
import { useT } from "~/lib/i18n";
import { requireUser } from "~/lib/session.server";
import { formatBytes } from "~/lib/format";
import {
  ArchiveIcon,
  AttachmentIcon,
  Avatar,
  Badge,
  BackIcon,
  Button,
  DownloadIcon,
  ErrorBanner,
  IconButton,
  MailOpenIcon,
  ReplyIcon,
  StarIcon,
  TimeText,
  TrashIcon,
} from "~/kit";
import { HtmlBody } from "./html-body";

// Reading pane — headers, body (HTML behind a sandbox, text otherwise),
// attachments, and the actions a mail client is expected to have.

export const loader = async ({ request, params }: Route.LoaderArgs) => {
  const user = await requireUser(request);
  const detail = await apiFetch<MessageDetail>(user.idToken, `/api/me/message/${params.id}`);
  return { detail, mailbox: params.mailbox ?? detail.mailbox };
};

export const action = async ({ request, params }: Route.ActionArgs) => {
  const user = await requireUser(request);
  const form = await request.formData();
  const intent = String(form.get("intent") ?? "");
  const id = params.id;
  const mailbox = params.mailbox ?? "INBOX";
  const back = `/mail/${encodeURIComponent(mailbox)}`;

  try {
    switch (intent) {
      case "delete":
        await apiFetch(user.idToken, `/api/me/message/${id}`, { method: "DELETE" });
        return redirect(back);
      case "archive":
        await apiFetch(user.idToken, `/api/me/message/${id}/move`, {
          method: "POST",
          body: { mailbox: "Archive" },
        });
        return redirect(back);
      case "unread":
        await apiFetch(user.idToken, `/api/me/message/${id}`, {
          method: "PATCH",
          body: { seen: false },
        });
        return redirect(back);
      case "flag":
        await apiFetch(user.idToken, `/api/me/message/${id}`, {
          method: "PATCH",
          body: { flagged: form.get("flagged") === "true" },
        });
        return { ok: true as const };
      default:
        return { ok: false as const, error: "unknown intent" };
    }
  } catch (e) {
    if (e instanceof ApiError) return { ok: false as const, error: e.message };
    throw e;
  }
};

export default function MessageDetailPage({ loaderData, actionData }: Route.ComponentProps) {
  const { detail, mailbox } = loaderData;
  const t = useT();
  const nav = useNavigation();
  const busy = nav.state !== "idle";
  const inTrash = detail.mailbox === "Trash";
  const sender = detail.fromAddr || t("mail.unknownSender");
  const back = `/mail/${encodeURIComponent(mailbox)}`;

  return (
    <article className="flex min-h-0 w-full flex-col">
      <header className="flex h-13 shrink-0 items-center gap-1 border-b border-line px-3">
        <Link
          to={back}
          className="mr-1 flex size-8 items-center justify-center rounded-md text-ink-3 hover:bg-raised hover:text-ink md:hidden"
          aria-label={t("common.back")}
        >
          <BackIcon className="size-4" />
        </Link>
        <Button
          variant="subtle"
          size="sm"
          onClick={() => {
            window.location.href = `${back}/compose?reply=${detail.id}`;
          }}
          type="button"
        >
          <ReplyIcon className="size-3.5" />
          {t("mail.reply")}
        </Button>
        <div className="ml-auto flex items-center gap-0.5">
          <Form method="post">
            <input type="hidden" name="intent" value="flag" />
            <input type="hidden" name="flagged" value={detail.flagged ? "false" : "true"} />
            <IconButton
              label={detail.flagged ? t("mail.unflag") : t("mail.flag")}
              size="iconSm"
              disabled={busy}
            >
              <StarIcon
                className={`size-4 ${detail.flagged ? "fill-warn text-warn" : ""}`}
              />
            </IconButton>
          </Form>
          <Form method="post">
            <input type="hidden" name="intent" value="unread" />
            <IconButton label={t("mail.markUnread")} size="iconSm" disabled={busy}>
              <MailOpenIcon className="size-4" />
            </IconButton>
          </Form>
          {detail.mailbox !== "Archive" && (
            <Form method="post">
              <input type="hidden" name="intent" value="archive" />
              <IconButton label={t("mail.archive")} size="iconSm" disabled={busy}>
                <ArchiveIcon className="size-4" />
              </IconButton>
            </Form>
          )}
          <Form method="post">
            <input type="hidden" name="intent" value="delete" />
            <IconButton
              label={inTrash ? t("mail.deleteForever") : t("mail.delete")}
              size="iconSm"
              variant="danger"
              disabled={busy}
              confirmMessage={inTrash ? t("mail.confirmDeleteForever") : undefined}
            >
              <TrashIcon className="size-4" />
            </IconButton>
          </Form>
        </div>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto scroll-thin">
        <div className="mx-auto flex max-w-3xl flex-col gap-4 px-5 py-5">
          <ErrorBanner message={actionData && !actionData.ok ? actionData.error : null} />

          <div>
            <h1 className="text-xl font-semibold tracking-tight text-ink">
              {detail.subject || t("mail.noSubject")}
            </h1>
            <div className="mt-3 flex items-start gap-3">
              <Avatar label={sender} seed={sender} size={36} />
              <div className="min-w-0 flex-1 text-xs">
                <p className="truncate font-medium text-ink">{sender}</p>
                <p className="truncate text-ink-3">
                  {t("mail.to")} {(detail.toList ?? []).join(", ") || "—"}
                </p>
                {detail.ccList && detail.ccList.length > 0 && (
                  <p className="truncate text-ink-3">
                    {t("mail.cc")} {detail.ccList.join(", ")}
                  </p>
                )}
              </div>
              <TimeText
                value={detail.date ?? detail.internalDate}
                mode="full"
                className="shrink-0 text-xs text-ink-faint"
              />
            </div>
          </div>

          {detail.parseWarn && <ErrorBanner message={t("mail.parseWarn")} />}

          <div className="border-t border-line pt-4">
            {detail.htmlBody ? (
              <HtmlBody html={detail.htmlBody} />
            ) : (
              <pre className="font-sans text-sm leading-6 whitespace-pre-wrap text-ink">
                {detail.textBody}
              </pre>
            )}
          </div>

          {detail.attachmentList.length > 0 && (
            <div className="flex flex-col gap-2 border-t border-line pt-4">
              <p className="flex items-center gap-1.5 text-xs font-medium text-ink-2">
                <AttachmentIcon className="size-3.5" />
                {t("mail.attachment", { count: detail.attachmentList.length })}
              </p>
              <ul className="flex flex-wrap gap-2">
                {detail.attachmentList.map((a) => (
                  <li key={a.index}>
                    <a
                      href={`/mail-file/${detail.id}/attachment/${a.index}`}
                      download
                      className="flex items-center gap-2 rounded-md border border-line bg-surface px-3 py-2 text-xs text-ink-2 hover:border-line-strong hover:text-ink"
                    >
                      <DownloadIcon className="size-3.5 text-ink-3" />
                      <span className="max-w-48 truncate">
                        {a.filename || `attachment-${a.index}`}
                      </span>
                      <Badge tone="muted">{formatBytes(a.sizeBytes)}</Badge>
                    </a>
                  </li>
                ))}
              </ul>
            </div>
          )}

          <div className="pt-2">
            <a
              href={`/mail-file/${detail.id}/raw`}
              download
              className="text-xs text-ink-faint hover:text-ink-2 hover:underline"
            >
              {t("mail.raw")}
            </a>
          </div>
        </div>
      </div>
    </article>
  );
}
