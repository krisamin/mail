import { Form, redirect, useNavigation } from "react-router";
import type { Route } from "./+types/compose";
import {
  ApiError,
  apiFetch,
  type Address,
  type MessageDetail,
  type SendResult,
} from "~/lib/api.server";
import { translate } from "~/i18n";
import { useT } from "~/lib/i18n";
import { getLocale } from "~/lib/locale.server";
import { requireUser } from "~/lib/session.server";
import { Button, ButtonLink, Card, ErrorBanner, PageTitle, SelectInput, TextInput } from "~/components";

// Compose — plain-text mail. From is a select over the user's owned
// addresses; ?replyTo=<id> pre-fills a reply (subject/recipient/threading).

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await requireUser(request);
  const url = new URL(request.url);
  const replyTo = url.searchParams.get("replyTo");

  const addressList =
    (await apiFetch<Address[]>(user.idToken, "/api/me/address")) ?? [];
  // wildcard addresses are not directly usable as a From value
  const fromList = addressList
    .filter((a) => a.localPart !== "*")
    .map((a) => `${a.localPart}@${a.domainName}`);

  let reply: {
    id: string;
    to: string;
    subject: string;
    quote: string;
  } | null = null;
  if (replyTo) {
    const orig = await apiFetch<MessageDetail>(user.idToken, `/api/me/message/${replyTo}`);
    const subject = orig.subject.startsWith("Re: ") ? orig.subject : `Re: ${orig.subject}`;
    const quote = orig.textBody
      ? `\n\n> ${orig.textBody.split("\n").join("\n> ")}`
      : "";
    reply = {
      id: orig.id,
      to: orig.replyTo || extractAddress(orig.fromAddr),
      subject,
      quote,
    };
  }
  return { fromList, reply };
};

export const action = async ({ request }: Route.ActionArgs) => {
  const user = await requireUser(request);
  const form = await request.formData();
  const locale = await getLocale(request);

  const splitAddressList = (v: FormDataEntryValue | null): string[] =>
    String(v ?? "")
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);

  try {
    const result = await apiFetch<SendResult>(user.idToken, "/api/me/send", {
      method: "POST",
      body: {
        from: String(form.get("from") ?? ""),
        toList: splitAddressList(form.get("to")),
        ccList: splitAddressList(form.get("cc")),
        subject: String(form.get("subject") ?? ""),
        textBody: String(form.get("body") ?? ""),
        inReplyTo: String(form.get("inReplyTo") ?? ""),
      },
      timeoutMs: 30_000, // queue insert + local delivery can take a moment
    });
    const message = translate(locale, "webmail.sent", {
      delivered: result.delivered,
      queued: result.queued,
    });
    return redirect(`/mail/Sent?sent=${encodeURIComponent(message)}`);
  } catch (e) {
    if (e instanceof ApiError) return { ok: false as const, error: e.message };
    throw e;
  }
};

export default function Compose({ loaderData, actionData }: Route.ComponentProps) {
  const { fromList, reply } = loaderData;
  const t = useT();
  const nav = useNavigation();
  const busy = nav.state !== "idle";

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between">
        <PageTitle title={t("webmail.compose")} />
        <ButtonLink to="/mail/INBOX" variant="chip">
          ← {t("webmail.backToList")}
        </ButtonLink>
      </div>

      <ErrorBanner message={actionData && !actionData.ok ? actionData.error : null} />

      <Card className="p-4">
        <Form method="post" className="flex flex-col gap-3">
          {reply && <input type="hidden" name="inReplyTo" value={reply.id} />}
          <label className="flex flex-col gap-1 text-xs text-text-2">
            {t("webmail.from")}
            <SelectInput name="from" required>
              {fromList.map((a) => (
                <option key={a} value={a}>
                  {a}
                </option>
              ))}
            </SelectInput>
          </label>
          <label className="flex flex-col gap-1 text-xs text-text-2">
            {t("webmail.to")}
            <TextInput
              name="to"
              required
              defaultValue={reply?.to ?? ""}
              placeholder={t("webmail.toPlaceholder")}
            />
          </label>
          <label className="flex flex-col gap-1 text-xs text-text-2">
            {t("webmail.cc")}
            <TextInput name="cc" placeholder={t("webmail.ccPlaceholder")} />
          </label>
          <label className="flex flex-col gap-1 text-xs text-text-2">
            {t("webmail.subject")}
            <TextInput
              name="subject"
              defaultValue={reply?.subject ?? ""}
              placeholder={t("webmail.subjectPlaceholder")}
            />
          </label>
          <label className="flex flex-col gap-1 text-xs text-text-2">
            {t("webmail.body")}
            <textarea
              name="body"
              rows={14}
              defaultValue={reply?.quote ?? ""}
              className="rounded-md border border-line bg-bg-1 px-3 py-2 font-mono text-sm outline-none focus:border-accent"
            />
          </label>
          <div>
            <Button pending={busy}>{t("webmail.send")}</Button>
          </div>
        </Form>
      </Card>
    </div>
  );
}

/** "Name <addr>" → addr; bare address passes through. */
const extractAddress = (s: string): string => {
  const m = s.match(/<([^>]+)>/);
  return m?.[1] ?? s;
};
