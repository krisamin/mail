import { useEffect, useMemo, useRef, useState } from "react";
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
    padding: 14px 16px;
  }
  a { color: #2f6fe4; }
  img { max-width: 100%; height: auto; }
  table { max-width: 100% !important; }
  blockquote {
    margin: 0 0 0 0.5rem; padding-left: 0.75rem;
    border-left: 2px solid #c9ced8;
    color: #454b55;
  }
</style></head><body>${html}</body></html>`;
};

export const HtmlBody = ({ html }: { html: string }) => {
  const t = useT();
  const frameRef = useRef<HTMLIFrameElement>(null);
  const [remote, setRemote] = useState(false);
  const [height, setHeight] = useState(240);

  const doc = useMemo(() => frameDoc(html, remote), [html, remote]);

  // size the frame to its content; re-measure after images settle
  useEffect(() => {
    const frame = frameRef.current;
    if (!frame) return;
    const measure = () => {
      const body = frame.contentDocument?.body;
      if (body) setHeight(Math.min(body.scrollHeight + 4, 20000));
    };
    measure();
    const id = setInterval(measure, 400);
    const stop = setTimeout(() => clearInterval(id), 4000);
    return () => {
      clearInterval(id);
      clearTimeout(stop);
    };
  }, [doc]);

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
      <div className="overflow-hidden rounded-lg border border-line bg-white">
        <iframe
          ref={frameRef}
          title={t("mail.htmlBody")}
          sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox"
          srcDoc={doc}
          style={{ height }}
          className="block w-full border-0"
        />
      </div>
    </div>
  );
};
