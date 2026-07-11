import { Form, redirect, useNavigation } from "react-router";
import type { Route } from "./+types/detail";
import { ApiError, apiFetch, type MessageDetail } from "~/lib/api.server";
import { useT } from "~/lib/i18n";
import { requireUser } from "~/lib/session.server";
import { Badge, Button, ButtonLink, Card, ErrorBanner, TimeText } from "~/components";
import { folderLabel } from "./layout";

// Message detail — text body (HTML mail shows its text alternative or a
// notice; raw HTML is never rendered in the app origin), attachment
// downloads, reply/archive/delete/flag actions.

export const loader = async ({ request, params }: Route.LoaderArgs) => {
  const user = await requireUser(request);
  const detail = await apiFetch<MessageDetail>(user.idToken, `/api/me/message/${params.id}`);
  return { detail, mailbox: params.mailbox ?? detail.mailbox };
};

export const action = async ({ request, params }: Route.ActionArgs) => {
  const user = await requireUser(request);
  const form = await request.formData();
  const intent = form.get("intent");
  const id = params.id;
  const mailbox = params.mailbox ?? "INBOX";

  try {
    switch (intent) {
      case "delete": {
        await apiFetch(user.idToken, `/api/me/message/${id}`, { method: "DELETE" });
        return redirect(`/mail/${encodeURIComponent(mailbox)}`);
      }
      case "archive": {
        await apiFetch(user.idToken, `/api/me/message/${id}/move`, {
          method: "POST",
          body: { mailbox: "Archive" },
        });
        return redirect(`/mail/${encodeURIComponent(mailbox)}`);
      }
      case "unread": {
        await apiFetch(user.idToken, `/api/me/message/${id}`, {
          method: "PATCH",
          body: { seen: false },
        });
        return redirect(`/mail/${encodeURIComponent(mailbox)}`);
      }
      case "flag": {
        await apiFetch(user.idToken, `/api/me/message/${id}`, {
          method: "PATCH",
          body: { flagged: form.get("flagged") === "true" },
        });
        return { ok: true as const };
      }
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
  // no text alternative in an HTML-only mail → show the notice
  const bodyText = detail.textBody || "";

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between gap-3">
        <ButtonLink to={`/mail/${encodeURIComponent(mailbox)}`} variant="chip">
          ← {t("webmail.backToList")}
        </ButtonLink>
        <div className="flex items-center gap-2">
          <ButtonLink
            to={`/mail/compose?replyTo=${detail.id}`}
            variant="outline"
            className="!px-3 !py-1.5 text-xs"
          >
            {t("webmail.reply")}
          </ButtonLink>
          <Form method="post">
            <input type="hidden" name="intent" value="flag" />
            <input type="hidden" name="flagged" value={detail.flagged ? "false" : "true"} />
            <Button variant="chip" pending={busy}>
              {detail.flagged ? t("webmail.unflag") : `★ ${t("webmail.flag")}`}
            </Button>
          </Form>
          <Form method="post">
            <input type="hidden" name="intent" value="unread" />
            <Button variant="chip" pending={busy}>
              {t("webmail.markUnread")}
            </Button>
          </Form>
          {detail.mailbox !== "Archive" && (
            <Form method="post">
              <input type="hidden" name="intent" value="archive" />
              <Button variant="chip" pending={busy}>
                {t("webmail.archive")}
              </Button>
            </Form>
          )}
          <Form method="post">
            <input type="hidden" name="intent" value="delete" />
            <Button
              variant="linkDanger"
              pending={busy}
              confirmMessage={inTrash ? t("webmail.confirmDeleteForever") : undefined}
            >
              {inTrash ? t("webmail.deleteForever") : t("webmail.delete")}
            </Button>
          </Form>
        </div>
      </div>

      <ErrorBanner message={actionData && !actionData.ok ? actionData.error : null} />

      <Card className="p-4">
        <div className="flex items-start justify-between gap-3">
          <h1 className="text-lg font-bold">
            {detail.flagged && <span className="mr-1 text-warn">★</span>}
            {detail.subject || t("webmail.noSubject")}
          </h1>
          <TimeText
            value={detail.date ?? detail.internalDate}
            className="shrink-0 text-xs text-text-2"
          />
        </div>
        <dl className="mt-3 flex flex-col gap-1 text-xs">
          <div className="flex gap-2">
            <dt className="w-16 shrink-0 text-text-2">{t("webmail.from")}</dt>
            <dd className="font-mono text-text-1">{detail.fromAddr}</dd>
          </div>
          {detail.toList && detail.toList.length > 0 && (
            <div className="flex gap-2">
              <dt className="w-16 shrink-0 text-text-2">{t("webmail.to")}</dt>
              <dd className="font-mono text-text-1">{detail.toList.join(", ")}</dd>
            </div>
          )}
          {detail.ccList && detail.ccList.length > 0 && (
            <div className="flex gap-2">
              <dt className="w-16 shrink-0 text-text-2">{t("webmail.cc")}</dt>
              <dd className="font-mono text-text-1">{detail.ccList.join(", ")}</dd>
            </div>
          )}
        </dl>
      </Card>

      {detail.parseWarn && <ErrorBanner message={t("webmail.parseWarn")} />}

      <Card className="p-4">
        {bodyText ? (
          <pre className="whitespace-pre-wrap break-words font-sans text-sm text-text-0">
            {bodyText}
          </pre>
        ) : (
          <p className="text-sm text-text-2">{detail.htmlBody ? t("webmail.htmlNotice") : ""}</p>
        )}
      </Card>

      {detail.attachmentList.length > 0 && (
        <Card className="p-4">
          <h2 className="text-sm font-medium text-text-1">{t("webmail.attachment")}</h2>
          <ul className="mt-2 flex flex-col gap-1.5">
            {detail.attachmentList.map((a) => (
              <li key={a.index} className="flex items-center gap-2 text-sm">
                <a
                  href={`/mail-file/${detail.id}/attachment/${a.index}`}
                  className="text-accent hover:underline"
                  download
                >
                  {a.filename || `attachment-${a.index}`}
                </a>
                <Badge tone="muted">{a.contentType}</Badge>
                <span className="text-xs text-muted">{formatSize(a.sizeBytes)}</span>
              </li>
            ))}
          </ul>
        </Card>
      )}

      <div>
        <a
          href={`/mail-file/${detail.id}/raw`}
          className="text-xs text-text-2 hover:text-text-1 hover:underline"
          download
        >
          {t("webmail.raw")}
        </a>
      </div>
    </div>
  );
}

const formatSize = (n: number): string => {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
};
