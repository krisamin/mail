import {
  Links,
  Meta,
  Outlet,
  Scripts,
  ScrollRestoration,
  isRouteErrorResponse,
} from "react-router";
import type { Route } from "./+types/root";
import "./app.css";

export const links: Route.LinksFunction = () => [];

export const meta: Route.MetaFunction = () => [
  { title: "mail" },
  { name: "viewport", content: "width=device-width, initial-scale=1, viewport-fit=cover" },
];

export default function App() {
  return (
    <html lang="ko" className="dark">
      <head>
        <meta charSet="utf-8" />
        <Meta />
        <Links />
      </head>
      <body>
        <Outlet />
        <ScrollRestoration />
        <Scripts />
      </body>
    </html>
  );
}

export function ErrorBoundary({ error }: Route.ErrorBoundaryProps) {
  let message = "오류";
  let detail = "알 수 없는 오류가 발생했어요.";
  if (isRouteErrorResponse(error)) {
    message = `${error.status}`;
    detail = error.status === 404 ? "페이지를 찾을 수 없어요." : (error.statusText ?? detail);
  } else if (error instanceof Error) {
    detail = error.message;
  }
  return (
    <html lang="ko" className="dark">
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
            홈으로
          </a>
        </main>
        <Scripts />
      </body>
    </html>
  );
}
