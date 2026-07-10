import type { Route } from "./+types/home";
import { getUser, isAdmin } from "~/lib/session.server";
import { ButtonLink, Card } from "~/components";
import { useT } from "~/lib/i18n";

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await getUser(request);
  return { user: user ? { name: user.name, email: user.email } : null, admin: isAdmin(user) };
};

export default function Home({ loaderData }: Route.ComponentProps) {
  const { user, admin } = loaderData;
  const t = useT();
  return (
    <main className="mx-auto flex min-h-dvh max-w-md flex-col items-center justify-center gap-6 p-8">
      <div className="text-center">
        <h1 className="text-4xl font-bold tracking-tight">mail</h1>
        <p className="mt-2 text-sm text-text-2">{t("home.tagline")}</p>
      </div>

      {user ? (
        <div className="flex w-full flex-col gap-3">
          <Card className="p-4 text-center">
            <p className="text-sm text-text-1">{user.name}</p>
            <p className="text-xs text-text-2">{user.email}</p>
          </Card>
          <ButtonLink to="/account">{t("nav.myAccount")}</ButtonLink>
          {admin && (
            <ButtonLink to="/admin" variant="outline">
              {t("nav.adminConsole")}
            </ButtonLink>
          )}
          <ButtonLink to="/logout" variant="outline">
            {t("common.logout")}
          </ButtonLink>
        </div>
      ) : (
        <ButtonLink to="/login" className="w-full">
          {t("common.login")}
        </ButtonLink>
      )}
    </main>
  );
}
