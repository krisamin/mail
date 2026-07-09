import type { Route } from "./+types/home";
import { getUser, isAdmin } from "~/lib/session.server";
import { ButtonLink, Card } from "~/components";

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
        <p className="mt-2 text-sm text-text-2">multi-tenant mail server</p>
      </div>

      {user ? (
        <div className="flex w-full flex-col gap-3">
          <Card className="p-4 text-center">
            <p className="text-sm text-text-1">{user.name}</p>
            <p className="text-xs text-text-2">{user.email}</p>
          </Card>
          <ButtonLink to="/account">내 계정</ButtonLink>
          {admin && (
            <ButtonLink to="/admin" variant="outline">
              관리 콘솔
            </ButtonLink>
          )}
          <ButtonLink to="/logout" variant="outline">
            로그아웃
          </ButtonLink>
        </div>
      ) : (
        <ButtonLink to="/login" className="w-full">
          로그인
        </ButtonLink>
      )}
    </main>
  );
}
