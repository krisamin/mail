import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useT } from "~/lib/i18n";
import { Button } from "~/kit";

// HTML mail rendering.
//
// Mail HTML is hostile by default, so it is shown behind three walls:
//   1. the server sanitises it (scripts, handlers, objects are gone),
//   2. it renders inside a sandboxed iframe — no scripts, no forms, no
//      top-level navigation, and it cannot reach this document,
//   3. a CSP inside the frame blocks remote loads, so trackers and remote
//      images stay silent until the reader asks for them.
//
// The frame keeps allow-same-origin (without allow-scripts nothing can run in
// it) purely so the parent can read scrollHeight and size the frame to the
// content; allow-popups lets a clicked link open in a new tab.
//
// ── Why the frame is always a white sheet ───────────────────
// A mail is a picture the sender painted: their own colours are baked into
// the markup, and most of the world paints for a white background. If we
// hand the frame our dark canvas and a light default ink, every mail that
// sets its own dark text turns invisible, and every mail that sets its own
// white panel gets our light ink on white. Repainting their markup to match
// our theme is guesswork that breaks newsletters.
//
// So the frame is a sheet of white paper laid on the reading pane, in both
// themes, with a border that makes it read as "this is what the sender
// sent". Plain-text mail has no colours of its own and keeps following the
// app theme.

const frameDoc = (html: string, remote: boolean): string => {
  const policy = remote
    ? "default-src 'none'; img-src https: http: data: cid:; style-src 'unsafe-inline'; font-src data:"
    : "default-src 'none'; img-src data: cid:; style-src 'unsafe-inline'; font-src data:";
  return `<!doctype html><html><head><meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="${policy}">
<base target="_blank">
<style>
  /* light: the sender's colours were chosen against white */
  :root { color-scheme: light; }
  html, body { margin: 0; padding: 0; background: #ffffff; }
  body {
    color: #14161a;
    font: 14px/1.6 ui-sans-serif, system-ui, -apple-system, "Segoe UI", Pretendard, sans-serif;
    word-break: break-word;
    overflow-wrap: anywhere;
  }
  /* The measured box. Padding lives here, and the overflow guard keeps a
     child's margin from escaping and making the height wrong. */
  #__mail {
    padding: 14px 16px;
    overflow: hidden;
  }
  a { color: #2f6fe4; }
  img { max-width: 100%; height: auto; }
  table { max-width: 100% !important; }
  blockquote {
    margin: 0 0 0 0.5rem; padding-left: 0.75rem;
    border-left: 2px solid #c9ced8;
    color: #454b55;
  }
</style></head><body><div id="__mail">${html}</div></body></html>`;
};

export const HtmlBody = ({ html }: { html: string }) => {
  const t = useT();
  const frameRef = useRef<HTMLIFrameElement>(null);
  const [remote, setRemote] = useState(false);
  // null until measured. A placeholder height would be a wrong size shown for
  // one beat and then corrected, which reads worse than a blank moment.
  const [height, setHeight] = useState<number | null>(null);

  const doc = useMemo(() => frameDoc(html, remote), [html, remote]);
  // lets the retry inside sizeToContent call the latest version of itself
  const sizeToContentRef = useRef<() => void>(() => {});

  // Reset when the document changes, so the previous mail's height is never
  // worn by the next one.
  useEffect(() => setHeight(null), [doc]);

  // Size the frame to its content. srcdoc parses synchronously, so the load
  // event fires with the layout already settled — that is the measurement
  // that matters, and it lands in the same frame the mail appears in.
  // A ResizeObserver keeps up with whatever arrives later (images decoding,
  // the reader allowing remote content).
  const sizeToContent = useCallback(() => {
    const frame = frameRef.current;
    const content = frame?.contentDocument?.getElementById("__mail");
    if (!content) return;
    const next = Math.min(Math.ceil(content.getBoundingClientRect().height), 20000);
    // Pre-layout reads come back as a few pixels of padding; publishing that
    // is what showed a sliver that then jumped to full size.
    if (next <= 32) {
      requestAnimationFrame(() => requestAnimationFrame(sizeToContentRef.current));
      return;
    }
    setHeight((current) => (current === next ? current : next));
  }, []);

  // The load event can arrive before the frame has laid its content out — the
  // body measured 12px there and 82px one frame later, which is exactly the
  // "resizes a beat late" being reported. Wait for a painted frame before the
  // first measurement, so the height it appears at is the height it keeps.
  sizeToContentRef.current = sizeToContent;

  const sizeWhenSettled = useCallback(() => {
    requestAnimationFrame(() => requestAnimationFrame(sizeToContent));
  }, [sizeToContent]);

  useEffect(() => {
    const frame = frameRef.current;
    if (!frame) return;
    sizeWhenSettled();

    const target = frame.contentDocument?.documentElement;
    if (!target || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(() => sizeToContent());
    observer.observe(target);
    // Images report their size only once decoded; body growth covers most of
    // it, but a fixed-size image swapping in does not always resize the root.
    const imageList = [...(frame.contentDocument?.images ?? [])];
    for (const image of imageList) {
      if (!image.complete) image.addEventListener("load", sizeToContent, { once: true });
    }
    return () => {
      observer.disconnect();
      for (const image of imageList) image.removeEventListener("load", sizeToContent);
    };
  }, [doc, sizeToContent, sizeWhenSettled]);

  const hasRemoteAsset = useMemo(
    () => /(<img[^>]+src=["']https?:)|(url\(https?:)/i.test(html),
    [html],
  );

  return (
    <div className="flex flex-col gap-2">
      {hasRemoteAsset && !remote && (
        <div className="flex items-center justify-between gap-3 rounded-md border border-line bg-raised px-3 py-2 text-xs text-ink-2">
          <span>{t("mail.remoteBlocked")}</span>
          <Button variant="subtle" size="sm" type="button" onClick={() => setRemote(true)}>
            {t("mail.showRemote")}
          </Button>
        </div>
      )}
      <div
        className="overflow-hidden rounded-lg border border-line bg-white"
        style={{ visibility: height === null ? "hidden" : "visible" }}
      >
        <iframe
          ref={frameRef}
          title={t("mail.htmlBody")}
          sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox"
          srcDoc={doc}
          onLoad={sizeWhenSettled}
          style={{ height: height ?? 0 }}
          className="block w-full border-0"
        />
      </div>
    </div>
  );
};
