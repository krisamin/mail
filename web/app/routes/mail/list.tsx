import { useEffect, useMemo, useRef, useState } from "react";
import {
  Link,
  Outlet,
  useFetcher,
  useLocation,
  useNavigate,
  useRevalidator,
  useSearchParams,
} from "react-router";
import type { Route } from "./+types/list";
import { apiFetch, type MessagePage, type MessageRow } from "~/lib/api.server";
import { useT } from "~/lib/i18n";
import { requireUser } from "~/lib/session.server";
import {
  ArchiveIcon,
  Avatar,
  Button,
  CheckIcon,
  EmptyState,
  IconButton,
  InboxIcon,
  MailOpenIcon,
  MoveIcon,
  RefreshIcon,
  SearchIcon,
  StarIcon,
  TimeText,
  TrashIcon,
} from "~/kit";
import { folderLabel } from "./layout";

// Message list — the left column of the reading layout.
//
// One loader fetch = one page. "Load more" appends client-side through a
// fetcher; the old implementation re-fetched every page up to the cursor on
// each render, which meant up to twenty sequential API calls to look at page
// twenty.

const PAGE_SIZE = 50;

export const loader = async ({ request, params }: Route.LoaderArgs) => {
  const user = await requireUser(request);
  const mailbox = params.mailbox ?? "INBOX";
  const url = new URL(request.url);
  const query = url.searchParams.get("q")?.trim() ?? "";
  const scope = url.searchParams.get("scope") ?? "";

  const search = new URLSearchParams({ mailbox, limit: String(PAGE_SIZE) });
  if (query) {
    search.set("q", query);
    if (scope === "all") search.set("scope", "all");
  }
  const page = await apiFetch<MessagePage>(user.idToken, `/api/me/message?${search}`);
  return { mailbox, query, scope, page };
};

export const action = async ({ request, params }: Route.ActionArgs) => {
  const user = await requireUser(request);
  const form = await request.formData();
  const intent = String(form.get("intent") ?? "");
  const idList = form.getAll("id").map(String);

  if (intent === "page") {
    // "load more" — one page beyond the given cursor
    const before = String(form.get("before") ?? "0");
    const search = new URLSearchParams({
      mailbox: params.mailbox ?? "INBOX",
      limit: String(PAGE_SIZE),
      before,
    });
    const page = await apiFetch<MessagePage>(user.idToken, `/api/me/message?${search}`);
    return { ok: true as const, page };
  }

  if (idList.length === 0) return { ok: false as const };
  await apiFetch(user.idToken, "/api/me/message/batch", {
    method: "POST",
    body: { idList, action: intent, mailbox: String(form.get("mailbox") ?? "") },
  });
  return { ok: true as const };
};

export default function MessageList({ loaderData, params }: Route.ComponentProps) {
  const { mailbox, query, page } = loaderData;
  const t = useT();
  const navigate = useNavigate();
  const location = useLocation();
  const revalidator = useRevalidator();
  const [searchParams, setSearchParams] = useSearchParams();
  const pager = useFetcher<typeof action>();
  const batch = useFetcher<typeof action>();

  const [extraList, setExtraList] = useState<MessageRow[]>([]);
  const [cursor, setCursor] = useState(page.nextBefore);
  const [selectedSet, setSelectedSet] = useState<Set<string>>(new Set());
  const [cursorIndex, setCursorIndex] = useState(0);
  // Opening a mail marks it read on the server, but the list was loaded
  // before that happened. Remember what was opened and draw it read at once
  // instead of waiting for the next load to catch up.
  const [openedSet, setOpenedSet] = useState<Set<string>>(new Set());
  const listRef = useRef<HTMLUListElement>(null);

  // a new mailbox/query resets the accumulated pages
  useEffect(() => {
    setExtraList([]);
    setCursor(page.nextBefore);
    setSelectedSet(new Set());
    setCursorIndex(0);
  }, [mailbox, query, page.nextBefore]);

  // appended pages arrive through the fetcher
  useEffect(() => {
    const next = pager.data;
    if (next && "page" in next && next.page) {
      setExtraList((prev) => [...prev, ...next.page.messageList]);
      setCursor(next.page.nextBefore);
    }
  }, [pager.data]);

  // batch actions refresh both the list and the folder counts
  useEffect(() => {
    if (batch.state === "idle" && batch.data?.ok) {
      setSelectedSet(new Set());
      revalidator.revalidate();
    }
  }, [batch.state, batch.data, revalidator]);

  // new mail without a manual refresh — poll only while visible and idle
  useEffect(() => {
    const id = setInterval(() => {
      if (document.visibilityState === "visible" && revalidator.state === "idle") {
        revalidator.revalidate();
      }
    }, 20_000);
    return () => clearInterval(id);
  }, [revalidator]);

  const rowList = useMemo(
    () => [...page.messageList, ...extraList],
    [page.messageList, extraList],
  );
  const openId = params.id;
  const childOpen = location.pathname !== `/mail/${encodeURIComponent(mailbox)}`;

  const runBatch = (intent: string, target?: string, idList?: string[]) => {
    const list = idList ?? [...selectedSet];
    if (list.length === 0) return;
    const form = new FormData();
    form.set("intent", intent);
    if (target) form.set("mailbox", target);
    for (const id of list) form.append("id", id);
    batch.submit(form, { method: "post" });
  };

  const toggle = (id: string) =>
    setSelectedSet((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  // keyboard: j/k move, enter opens, e archives, # deletes, / focuses search,
  // c composes. Ignored while typing in a field.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null;
      if (target && ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName)) return;
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      const row = rowList[cursorIndex];
      switch (e.key) {
        case "j":
          setCursorIndex((i) => Math.min(i + 1, rowList.length - 1));
          break;
        case "k":
          setCursorIndex((i) => Math.max(i - 1, 0));
          break;
        case "Enter":
          if (row) navigate(`/mail/${encodeURIComponent(mailbox)}/${row.id}`);
          break;
        case "x":
          if (row) toggle(row.id);
          break;
        case "e":
          if (row) runBatch("move", "Archive", [row.id]);
          break;
        case "#":
          if (row) runBatch("delete", undefined, [row.id]);
          break;
        case "c":
          navigate(`/mail/${encodeURIComponent(mailbox)}/compose`);
          break;
        case "/":
          e.preventDefault();
          document.getElementById("mail-search")?.focus();
          break;
        default:
          return;
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [rowList, cursorIndex, mailbox, navigate]);

  const selectedCount = selectedSet.size;

  return (
    <div className="flex min-h-0 w-full flex-1 overflow-hidden">
      <section
        className={`flex min-h-0 w-full flex-col border-line md:w-96 md:border-r ${
          childOpen ? "hidden md:flex" : "flex"
        }`}
      >
        <header className="flex h-13 shrink-0 items-center gap-2 border-b border-line px-3">
          {selectedCount > 0 ? (
            <>
              <span className="px-1 text-xs font-medium text-ink-2">
                {t("mail.selected", { count: selectedCount })}
              </span>
              <div className="ml-auto flex items-center gap-0.5">
                <IconButton label={t("mail.markRead")} size="iconSm" onClick={() => runBatch("seen")}>
                  <MailOpenIcon className="size-4" />
                </IconButton>
                <IconButton label={t("mail.archive")} size="iconSm" onClick={() => runBatch("move", "Archive")}>
                  <ArchiveIcon className="size-4" />
                </IconButton>
                <IconButton label={t("mail.delete")} size="iconSm" onClick={() => runBatch("delete")}>
                  <TrashIcon className="size-4" />
                </IconButton>
                <IconButton label={t("common.cancel")} size="iconSm" onClick={() => setSelectedSet(new Set())}>
                  <CheckIcon className="size-4" />
                </IconButton>
              </div>
            </>
          ) : (
            <>
              <form
                className="relative min-w-0 flex-1"
                onSubmit={(e) => {
                  e.preventDefault();
                  const value = new FormData(e.currentTarget).get("q");
                  const next = new URLSearchParams(searchParams);
                  if (value) next.set("q", String(value));
                  else next.delete("q");
                  setSearchParams(next);
                }}
              >
                <SearchIcon className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-ink-faint" />
                <input
                  id="mail-search"
                  name="q"
                  defaultValue={query}
                  placeholder={t("mail.searchPlaceholder", { folder: folderLabel(t, mailbox) })}
                  className="h-8 w-full rounded-md border border-line bg-canvas pr-2 pl-8 text-xs text-ink placeholder:text-ink-faint focus:border-brand focus:outline-none"
                />
              </form>
              <IconButton
                label={t("common.refresh")}
                size="iconSm"
                onClick={() => revalidator.revalidate()}
              >
                <RefreshIcon className={`size-4 ${revalidator.state !== "idle" ? "animate-spin" : ""}`} />
              </IconButton>
            </>
          )}
        </header>

        {query && (
          <div className="flex items-center justify-between gap-2 border-b border-line px-3 py-1.5 text-xs text-ink-3">
            <span>{t("mail.searchResult", { count: rowList.length })}</span>
            <Link
              to={`/mail/${encodeURIComponent(mailbox)}?q=${encodeURIComponent(query)}&scope=all`}
              className="text-brand hover:underline"
            >
              {t("mail.searchAll")}
            </Link>
          </div>
        )}

        <ul ref={listRef} className="min-h-0 flex-1 overflow-y-auto scroll-thin">
          {rowList.length === 0 ? (
            <li>
              <EmptyState
                icon={<InboxIcon className="size-7" strokeWidth={1.5} />}
                title={query ? t("mail.noResult") : t("mail.emptyFolder")}
              />
            </li>
          ) : (
            rowList.map((row, index) => {
              const active = openId === row.id;
              const seen = row.seen || openedSet.has(row.id);
              const sender = row.fromAddr || t("mail.unknownSender");
              return (
                <li key={row.id} className="border-b border-line/60">
                  <div
                    className={`group flex gap-2.5 px-3 py-2.5 ${
                      active ? "bg-brand-soft" : index === cursorIndex ? "bg-raised/60" : "hover:bg-raised/40"
                    }`}
                  >
                    <label className="flex shrink-0 items-start pt-0.5">
                      <input
                        type="checkbox"
                        checked={selectedSet.has(row.id)}
                        onChange={() => toggle(row.id)}
                        aria-label={t("mail.select")}
                        className="size-3.5 accent-[var(--tone-brand)]"
                      />
                    </label>
                    <Link
                      to={`/mail/${encodeURIComponent(row.mailbox ?? mailbox)}/${row.id}`}
                      onClick={() => {
                        setCursorIndex(index);
                        setOpenedSet((prev) => new Set(prev).add(row.id));
                      }}
                      className="flex min-w-0 flex-1 gap-2.5"
                    >
                      <Avatar label={sender} seed={sender} size={28} />
                      <div className="min-w-0 flex-1">
                        <div className="flex items-baseline gap-2">
                          <span
                            className={`min-w-0 flex-1 truncate text-xs ${
                              seen ? "text-ink-3" : "font-semibold text-ink"
                            }`}
                          >
                            {sender}
                          </span>
                          <TimeText value={row.internalDate} className="shrink-0 text-[11px] text-ink-faint" />
                        </div>
                        <p
                          className={`truncate text-sm ${seen ? "text-ink-2" : "font-medium text-ink"}`}
                        >
                          {row.flagged && <StarIcon className="mr-1 inline size-3 text-warn" />}
                          {row.subject || t("mail.noSubject")}
                        </p>
                        {row.preview && (
                          <p className="truncate text-xs text-ink-faint">{row.preview}</p>
                        )}
                      </div>
                      {!seen && <span className="mt-2 size-2 shrink-0 rounded-full bg-brand" />}
                    </Link>
                  </div>
                </li>
              );
            })
          )}
          {cursor > 0 && !query && (
            <li className="p-3">
              <Button
                variant="outline"
                size="sm"
                className="w-full"
                pending={pager.state !== "idle"}
                onClick={() =>
                  pager.submit({ intent: "page", before: String(cursor) }, { method: "post" })
                }
              >
                {t("mail.loadMore")}
              </Button>
            </li>
          )}
        </ul>
      </section>

      <section className={`min-h-0 min-w-0 flex-1 ${childOpen ? "flex" : "hidden md:flex"}`}>
        {childOpen ? (
          <Outlet />
        ) : (
          <div className="flex flex-1 items-center justify-center">
            <EmptyState
              icon={<MoveIcon className="size-7" strokeWidth={1.5} />}
              title={t("mail.pickMessage")}
            />
          </div>
        )}
      </section>
    </div>
  );
}
