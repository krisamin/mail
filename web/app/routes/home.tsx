import { Link } from "react-router";
import type { Route } from "./+types/home";
import { getUser, isAdmin } from "~/lib/session.server";

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await getUser(request);
  return { user: user ? { name: user.name, email: user.email } : null, admin: isAdmin(user) };
};

export default function Home({ loaderData }: Route.ComponentProps) {
  const { user, admin } = loaderData;
  return (
    <main className="mx-auto flex min-h-dvh max-w-md flex-col items-center justify-center gap-6 p-8">
      <div className="text-center">
        <h1 className="text-4xl font-bold tracking-tight">mail</h1>
        <p className="mt-2 text-sm text-text-2">krisam.in 메일 서버</p>
      </div>

      {user ? (
        <div className="flex w-full flex-col gap-3">
          <div className="rounded-md border border-line bg-bg-1 p-4 text-center">
            <p className="text-sm text-text-1">{user.name}</p>
            <p className="text-xs text-text-2">{user.email}</p>
          </div>
          <Link
            to="/account"
            className="rounded-md bg-accent px-4 py-2 text-center text-sm font-medium text-bg-0 hover:bg-accent-hover"
          >
            내 계정
          </Link>
          {admin && (
            <Link
              to="/admin"
              className="rounded-md border border-line px-4 py-2 text-center text-sm text-text-1 hover:bg-bg-2"
            >
              관리 콘솔
            </Link>
          )}
          <Link
            to="/logout"
            className="rounded-md border border-line px-4 py-2 text-center text-sm text-text-1 hover:bg-bg-2"
          >
            로그아웃
          </Link>
        </div>
      ) : (
        <Link
          to="/login"
          className="w-full rounded-md bg-accent px-4 py-2 text-center text-sm font-medium text-bg-0 hover:bg-accent-hover"
        >
          로그인
        </Link>
      )}
    </main>
  );
}
