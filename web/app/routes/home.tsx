import { redirect } from "react-router";
import type { Route } from "./+types/home";
import { getUser } from "~/lib/session.server";
import { ButtonLink } from "~/kit";
import { useT } from "~/lib/i18n";
import { BrandMark } from "~/shell/brand";

// Signed in, the root is the mailbox — a launcher page between the person and
// their mail is a click that says nothing. Signed out it is the door.
export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await getUser(request);
  if (user) throw redirect("/mail");
  return null;
};

export default function Home() {
  const t = useT();
  return (
    <main className="mx-auto flex min-h-dvh max-w-sm flex-col items-center justify-center gap-7 px-6">
      <div className="flex flex-col items-center gap-3 text-center">
        <BrandMark size={44} />
        <div>
          <h1 className="text-2xl font-semibold tracking-tight text-ink">mail</h1>
          <p className="mt-1.5 text-sm text-ink-3">{t("home.tagline")}</p>
        </div>
      </div>
      <ButtonLink to="/login" className="w-full">
        {t("common.signIn")}
      </ButtonLink>
    </main>
  );
}
