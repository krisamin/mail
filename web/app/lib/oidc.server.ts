// OIDC Authorization Code Flow 헬퍼 (라이브러리 없이 표준만).
// discovery → authorize URL → code 교환 → id_token 클레임 파싱.

const ISSUER = process.env.MAIL_OIDC_ISSUER ?? "http://localhost:8480/realms/mail";
const CLIENT_ID = process.env.MAIL_OIDC_CLIENT_ID ?? "mail-web";
const CLIENT_SECRET = process.env.MAIL_OIDC_CLIENT_SECRET ?? "mail-web-dev-secret";
// dev Keycloak은 client-level mapper로 클레임을 붙여서 openid만으로 충분.
// Authentik 등 실 IdP는 email/groups 클레임에 "openid profile email" 필요.
const SCOPE = process.env.MAIL_OIDC_SCOPE ?? "openid";

/** 리버스 프록시 뒤에선 요청 origin이 http로 보임 — 프로덕션은 env로 고정. */
export const publicOrigin = (request: Request): string =>
  process.env.MAIL_PUBLIC_ORIGIN ?? new URL(request.url).origin;

type Discovery = {
  authorization_endpoint: string;
  token_endpoint: string;
  end_session_endpoint?: string;
};

let discoveryCache: Discovery | null = null;

const discover = async (): Promise<Discovery> => {
  if (discoveryCache) return discoveryCache;
  const res = await fetch(`${ISSUER}/.well-known/openid-configuration`);
  if (!res.ok) throw new Error(`OIDC discovery 실패: ${res.status}`);
  discoveryCache = (await res.json()) as Discovery;
  return discoveryCache;
};

/** 로그인 시작 URL. state는 CSRF 방지용 랜덤 값 (세션에 저장해 콜백에서 대조). */
export const buildAuthorizeUrl = async (redirectUri: string, state: string): Promise<string> => {
  const d = await discover();
  const params = new URLSearchParams({
    response_type: "code",
    client_id: CLIENT_ID,
    redirect_uri: redirectUri,
    // 클레임(groups/email/username)은 IdP 설정에 따라 scope가 달라짐 —
    // dev Keycloak은 "openid"(client mapper), Authentik은 "openid profile email".
    scope: SCOPE,
    state,
  });
  return `${d.authorization_endpoint}?${params}`;
};

export type TokenSet = { idToken: string; accessToken: string };

/** 콜백 code를 토큰으로 교환. */
export const exchangeCode = async (code: string, redirectUri: string): Promise<TokenSet> => {
  const d = await discover();
  const res = await fetch(d.token_endpoint, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      grant_type: "authorization_code",
      client_id: CLIENT_ID,
      client_secret: CLIENT_SECRET,
      code,
      redirect_uri: redirectUri,
    }),
  });
  if (!res.ok) throw new Error(`토큰 교환 실패: ${res.status} ${await res.text()}`);
  const body = (await res.json()) as { id_token: string; access_token: string };
  return { idToken: body.id_token, accessToken: body.access_token };
};

export type IdClaims = {
  sub: string;
  name?: string;
  preferred_username?: string;
  email?: string;
  groups?: string[];
};

/** id_token 페이로드 디코드 (검증은 Go API가 JWKS로 다시 함 — 여기선 표시용). */
export const decodeClaims = (idToken: string): IdClaims => {
  const payload = idToken.split(".")[1];
  if (!payload) throw new Error("잘못된 토큰");
  const pad = payload + "=".repeat((4 - (payload.length % 4)) % 4);
  return JSON.parse(Buffer.from(pad, "base64url").toString()) as IdClaims;
};

/** IdP 로그아웃 URL (없으면 null — 세션만 지움). */
export const buildLogoutUrl = async (idToken: string, redirectUri: string): Promise<string | null> => {
  const d = await discover();
  if (!d.end_session_endpoint) return null;
  const params = new URLSearchParams({
    id_token_hint: idToken,
    post_logout_redirect_uri: redirectUri,
  });
  return `${d.end_session_endpoint}?${params}`;
};
