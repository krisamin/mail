import { useEffect, useRef, useState } from "react";
import { Form, Link, redirect, useFetcher, useNavigation, useSearchParams } from "react-router";
import type { Route } from "./+types/compose";
import {
  ApiError,
  apiFetch,
  type Address,
  type MessageDetail,
  type SendResult,
} from "~/lib/api.server";
import { useT } from "~/lib/i18n";
import { requireUser } from "~/lib/session.server";
import {
  BackIcon,
  Button,
  ErrorBanner,
  Field,
  SelectInput,
  SendIcon,
  TextArea,
  TextInput,
} from "~/kit";

// Compose — lives in the reading pane so the list stays put.
// Drafts save themselves: the body is posted to the Drafts mailbox a few
// seconds after typing stops, and again when the tab is left.

const DRAFT_DELAY_MS = 4000;

const quote = (detail: MessageDetail): string => {
  const when = detail.date ?? detail.internalDate;
  const head = `\n\n\n--- ${detail.fromAddr} (${when}) ---\n`;
  const body = (detail.textBody || "").split("\n").map((line) => `> ${line}`).join("\n");
  return head + body;
};

export const loader = async ({ request, params }: Route.LoaderArgs) => {
  const user = await requireUser(request);
  const url = new URL(request.url);
  const replyID = url.searchParams.get("reply");

  const addressList = await apiFetch<Address[]>(user.idToken, "/api/me/address").then((r) => r ?? []);
  const fromList = addressList
    .filter((a) => a.localPart !== "*")
    .map((a) => `${a.localPart}@${a.domainName}`);

  let draft = { to: "", cc: "", subject: "", text: "", inReplyTo: "" };
  if (replyID) {
    try {
      const detail = await apiFetch<MessageDetail>(user.idToken, `/api/me/message/${replyID}`);
      draft = {
        to: detail.replyTo || detail.fromAddr,
        cc: "",
        subject: detail.subject.toLowerCase().startsWith("re:")
          ? detail.subject
          : `Re: ${detail.subject}`,
        text: quote(detail),
        inReplyTo: detail.id,
      };
    } catch {
      // the message vanished (moved/deleted elsewhere) — compose empty
    }
  }
  return { fromList, draft, mailbox: params.mailbox ?? "INBOX" };
};

export const action = async ({ request, params }: Route.ActionArgs) => {
  const user = await requireUser(request);
  const form = await request.formData();
  const intent = String(form.get("intent") ?? "send");

  const splitList = (value: string): string[] =>
    value
      .split(/[,;]/)
      .map((s) => s.trim())
      .filter(Boolean);

  const from = String(form.get("from") ?? "");
  const toList = splitList(String(form.get("to") ?? ""));
  const ccList = splitList(String(form.get("cc") ?? ""));
  const subject = String(form.get("subject") ?? "");
  const text = String(form.get("text") ?? "");
  const inReplyTo = String(form.get("inReplyTo") ?? "");

  try {
    if (intent === "draft") {
      const saved = await apiFetch<{ id: string }>(user.idToken, "/api/me/draft", {
        method: "POST",
        body: {
          replaceId: String(form.get("replaceId") ?? ""),
          from,
          toList,
          ccList,
          subject,
          text,
          inReplyTo,
        },
      });
      return { ok: true as const, draftId: saved.id, savedAt: Date.now() };
    }

    if (toList.length === 0) {
      return { ok: false as const, error: "recipient required" };
    }
    const result = await apiFetch<SendResult>(user.idToken, "/api/me/send", {
      method: "POST",
      body: { from, toList, ccList, subject, textBody: text, inReplyTo },
    });
    // a sent draft is no longer a draft
    const replaceID = String(form.get("replaceId") ?? "");
    if (replaceID) {
      await apiFetch(user.idToken, `/api/me/message/${replaceID}`, { method: "DELETE" }).catch(
        () => undefined,
      );
    }
    const box = encodeURIComponent(params.mailbox ?? "INBOX");
    return redirect(`/mail/${box}?sent=${result.delivered + result.queued}`);
  } catch (e) {
    if (e instanceof ApiError) return { ok: false as const, error: e.message };
    throw e;
  }
};

export default function Compose({ loaderData, actionData }: Route.ComponentProps) {
  const { fromList, draft, mailbox } = loaderData;
  const t = useT();
  const nav = useNavigation();
  const drafter = useFetcher<typeof action>();
  const formRef = useRef<HTMLFormElement>(null);
  const [draftID, setDraftID] = useState("");
  const [dirty, setDirty] = useState(false);
  const [searchParams] = useSearchParams();
  const sending = nav.state !== "idle";

  useEffect(() => {
    const saved = drafter.data;
    if (saved && "draftId" in saved && saved.draftId) setDraftID(saved.draftId);
  }, [drafter.data]);

  // autosave: a pause in typing saves the draft, and so does leaving the page
  useEffect(() => {
    if (!dirty) return;
    const id = setTimeout(() => {
      const form = formRef.current;
      if (!form) return;
      const body = new FormData(form);
      body.set("intent", "draft");
      body.set("replaceId", draftID);
      drafter.submit(body, { method: "post" });
      setDirty(false);
    }, DRAFT_DELAY_MS);
    return () => clearTimeout(id);
  }, [dirty, draftID, drafter]);

  const savedLabel =
    drafter.state !== "idle"
      ? t("mail.draftSaving")
      : draftID
        ? t("mail.draftSaved")
        : "";

  return (
    <div className="flex min-h-0 w-full flex-col">
      <header className="flex h-13 shrink-0 items-center gap-2 border-b border-line px-3">
        <Link
          to={`/mail/${encodeURIComponent(mailbox)}`}
          className="flex size-8 items-center justify-center rounded-md text-ink-3 hover:bg-raised hover:text-ink md:hidden"
          aria-label={t("common.back")}
        >
          <BackIcon className="size-4" />
        </Link>
        <h1 className="text-sm font-semibold text-ink">
          {searchParams.get("reply") ? t("mail.replyTitle") : t("mail.composeTitle")}
        </h1>
        <span className="ml-auto text-xs text-ink-faint">{savedLabel}</span>
      </header>

      <Form
        method="post"
        ref={formRef}
        onChange={() => setDirty(true)}
        className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto scroll-thin px-5 py-4"
      >
        <input type="hidden" name="inReplyTo" defaultValue={draft.inReplyTo} />
        <input type="hidden" name="replaceId" value={draftID} readOnly />
        <ErrorBanner message={actionData && !actionData.ok ? actionData.error : null} />

        <Field label={t("mail.from")}>
          <SelectInput name="from" defaultValue={fromList[0] ?? ""}>
            {fromList.map((address) => (
              <option key={address} value={address}>
                {address}
              </option>
            ))}
          </SelectInput>
        </Field>
        <Field label={t("mail.to")} hint={t("mail.recipientHint")}>
          <TextInput name="to" defaultValue={draft.to} autoComplete="off" required />
        </Field>
        <Field label={t("mail.cc")}>
          <TextInput name="cc" defaultValue={draft.cc} autoComplete="off" />
        </Field>
        <Field label={t("mail.subject")}>
          <TextInput name="subject" defaultValue={draft.subject} />
        </Field>
        <div className="flex min-h-0 flex-1 flex-col gap-1.5">
          <label htmlFor="compose-body" className="text-xs font-medium text-ink-2">
            {t("mail.body")}
          </label>
          <TextArea
            id="compose-body"
            name="text"
            defaultValue={draft.text}
            rows={14}
            className="min-h-64 flex-1 font-sans"
          />
        </div>
        <div className="flex items-center gap-2 pb-2">
          <Button name="intent" value="send" pending={sending}>
            <SendIcon className="size-4" />
            {t("mail.send")}
          </Button>
          <Button
            name="intent"
            value="draft"
            variant="outline"
            formNoValidate
            pending={drafter.state !== "idle"}
          >
            {t("mail.saveDraft")}
          </Button>
          <Link
            to={`/mail/${encodeURIComponent(mailbox)}`}
            className="ml-auto text-xs text-ink-3 hover:text-ink"
          >
            {t("common.cancel")}
          </Link>
        </div>
      </Form>
    </div>
  );
}
