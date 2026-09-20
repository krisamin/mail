import type { ReactNode } from "react";
import {
  Links,
  Meta,
  Outlet,
  Scripts,
  ScrollRestoration,
  isRouteErrorResponse,
  useRouteLoaderData,
} from "react-router";
import type { Route } from "./+types/root";
import { translate } from "~/i18n";
import { I18nProvider } from "~/lib/i18n";
import type { Locale } from "~/lib/locale";
import { resolveLocale } from "~/lib/locale.server";
import { readPreference, themeFromCookie } from "~/lib/preference.server";
import { getUser } from "~/lib/session.server";
import { THEME_BOOTSTRAP, themeClass, type Theme } from "~/lib/theme";
import "./app.css";

export const meta: Route.MetaFunction = () => [
  { title: "mail" },
  { name: "viewport", content: "width=device-width, initial-scale=1, viewport-fit=cover" },
  { name: "color-scheme", content: "dark light" },
];

// The root loader resolves language and theme before anything renders:
// the account preference wins, with the cookie mirror covering signed-out
// visitors and the very first paint.
export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await getUser(request);
  const preference = await readPreference(user);
  const theme: Theme = user ? preference.theme : themeFromCookie(request);
  return { locale: await resolveLocale(request, preference.locale), theme };
};

const Document = ({
  locale,
  theme,
  children,
}: {
  locale: Locale;
  theme: Theme;
  children: ReactNode;
}) => (
  // the bootstrap script may flip the class before React hydrates
  <html lang={locale} className={themeClass(theme)} suppressHydrationWarning>
    <head>
      <meta charSet="utf-8" />
      <Meta />
      <Links />
      {/* "system" is only knowable in the browser — fix the class before paint. */}
      <script dangerouslySetInnerHTML={{ __html: THEME_BOOTSTRAP }} />
    </head>
    <body>
      <I18nProvider locale={locale}>{children}</I18nProvider>
      <ScrollRestoration />
      <Scripts />
    </body>
  </html>
);

export default function App({ loaderData }: Route.ComponentProps) {
  return (
    <Document locale={loaderData.locale} theme={loaderData.theme}>
      <Outlet />
    </Document>
  );
}

export function ErrorBoundary({ error }: Route.ErrorBoundaryProps) {
  const data = useRouteLoaderData<typeof loader>("root");
  const locale: Locale = data?.locale ?? "en";

  let message = translate(locale, "error.title");
  let detail = translate(locale, "error.unknown");
  if (isRouteErrorResponse(error)) {
    message = `${error.status}`;
    detail =
      error.status === 404
        ? translate(locale, "error.notFound")
        : error.data || error.statusText || detail;
  } else if (error instanceof Error) {
    detail = error.message;
  }
  return (
    <Document locale={locale} theme={data?.theme ?? "system"}>
      <main className="mx-auto flex min-h-dvh max-w-md flex-col items-center justify-center gap-2 p-8 text-center">
        <h1 className="text-3xl font-bold text-ink">{message}</h1>
        <p className="text-sm text-ink-3">{detail}</p>
        <a href="/" className="mt-4 text-sm text-brand hover:underline">
          {translate(locale, "error.home")}
        </a>
      </main>
    </Document>
  );
}
