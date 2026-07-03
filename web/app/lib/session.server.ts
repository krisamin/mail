import { createCookieSessionStorage } from "react-router";

// 세션 쿠키 — id_token(짧음)과 유저 요약만 담는다.
// dev 기본 시크릿, 프로덕션에선 SESSION_SECRET 필수.
const secret = process.env.SESSION_SECRET ?? "mail-dev-session-secret";

export const sessionStorage = createCookieSessionStorage({
  cookie: {
    name: "__mail_session",
    httpOnly: true,
    path: "/",
    sameSite: "lax",
    secrets: [secret],
    secure: process.env.NODE_ENV === "production",
    maxAge: 60 * 60 * 8, // 8h
  },
});

export type SessionUser = {
  sub: string;
  name: string;
  email: string;
  groups: string[];
  idToken: string;
};

export const getSession = (request: Request) =>
  sessionStorage.getSession(request.headers.get("Cookie"));

export const getUser = async (request: Request): Promise<SessionUser | null> => {
  const session = await getSession(request);
  const user = session.get("user") as SessionUser | undefined;
  return user ?? null;
};

export const ADMIN_GROUP = process.env.MAIL_ADMIN_GROUP ?? "mail-admin";

export const isAdmin = (user: SessionUser | null): boolean =>
  !!user && user.groups.some((g) => g === ADMIN_GROUP || g === `/${ADMIN_GROUP}`);
