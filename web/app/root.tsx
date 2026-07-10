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
import { getLocale } from "~/lib/locale.server";
import "./app.css";

export const links: Route.LinksFunction = () => [];

export const meta: Route.MetaFunction = () => [
  { title: "mail" },
  { name: "viewport", content: "width=device-width, initial-scale=1, viewport-fit=cover" },
];

// The root loader resolves the locale (cookie → Accept-Language) so SSR and
// hydration agree on the language before anything renders.
export const loader = async ({ request }: Route.LoaderArgs) => ({
  locale: await getLocale(request),
});

export default function App({ loaderData }: Route.ComponentProps) {
  const { locale } = loaderData;
  return (
    <html lang={locale} className="dark">
      <head>
        <meta charSet="utf-8" />
        <Meta />
        <Links />
      </head>
      <body>
        <I18nProvider locale={locale}>
          <Outlet />
        </I18nProvider>
        <ScrollRestoration />
        <Scripts />
      </body>
    </html>
  );
}

export function ErrorBoundary({ error }: Route.ErrorBoundaryProps) {
  // The root loader may not have run when an error boundary renders,
  // so fall back to English if the locale is unavailable.
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
    <html lang={locale} className="dark">
      <head>
        <meta charSet="utf-8" />
        <Meta />
        <Links />
      </head>
      <body>
        <main className="mx-auto flex min-h-dvh max-w-md flex-col items-center justify-center gap-2 p-8">
          <h1 className="text-3xl font-bold text-text-0">{message}</h1>
          <p className="text-sm text-text-2">{detail}</p>
          <a href="/" className="mt-4 text-sm text-accent hover:text-accent-hover">
            {translate(locale, "error.home")}
          </a>
        </main>
        <Scripts />
      </body>
    </html>
  );
}
