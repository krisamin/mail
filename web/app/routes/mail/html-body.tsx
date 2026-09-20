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

const frameDoc = (html: string, remote: boolean, dark: boolean): string => {
  const policy = remote
    ? "default-src 'none'; img-src https: http: data: cid:; style-src 'unsafe-inline'; font-src data:"
    : "default-src 'none'; img-src data: cid:; style-src 'unsafe-inline'; font-src data:";
  const ink = dark ? "#edeff2" : "#14161a";
  const link = dark ? "#6aa6ff" : "#2f6fe4";
  return `<!doctype html><html><head><meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="${policy}">
<base target="_blank">
<style>
  :root { color-scheme: ${dark ? "dark" : "light"}; }
  html, body { margin: 0; padding: 0; background: transparent; }
  body {
    color: ${ink};
    font: 14px/1.6 ui-sans-serif, system-ui, -apple-system, "Segoe UI", Pretendard, sans-serif;
    word-break: break-word;
    overflow-wrap: anywhere;
  }
  a { color: ${link}; }
  img { max-width: 100%; height: auto; }
  table { max-width: 100% !important; }
  blockquote {
    margin: 0 0 0 0.5rem; padding-left: 0.75rem;
    border-left: 2px solid ${dark ? "#333840" : "#c9ced8"};
    color: ${dark ? "#b4bac3" : "#454b55"};
  }
</style></head><body>${html}</body></html>`;
};

export const HtmlBody = ({ html }: { html: string }) => {
  const t = useT();
  const frameRef = useRef<HTMLIFrameElement>(null);
  const [remote, setRemote] = useState(false);
  const [height, setHeight] = useState(240);
  const [dark, setDark] = useState(true);

  useEffect(() => {
    setDark(document.documentElement.classList.contains("dark"));
  }, []);

  const doc = useMemo(() => frameDoc(html, remote, dark), [html, remote, dark]);

  // size the frame to its content; re-measure after images settle
  useEffect(() => {
    const frame = frameRef.current;
    if (!frame) return;
    const measure = () => {
      const body = frame.contentDocument?.body;
      if (body) setHeight(Math.min(body.scrollHeight + 16, 20000));
    };
    measure();
    const id = setInterval(measure, 400);
    const stop = setTimeout(() => clearInterval(id), 4000);
    return () => {
      clearInterval(id);
      clearTimeout(stop);
    };
  }, [doc]);

  const hasRemoteAsset = useMemo(() => /(<img[^>]+src=["']https?:)|(url\(https?:)/i.test(html), [html]);

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
      <iframe
        ref={frameRef}
        title={t("mail.htmlBody")}
        sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox"
        srcDoc={doc}
        style={{ height }}
        className="w-full border-0"
      />
    </div>
  );
};
