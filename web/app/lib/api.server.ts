// Go Admin API 호출 헬퍼 — RR7 loader/action에서 서버사이드로만 호출.
// 세션의 id_token을 Bearer로 전달, Go 쪽이 JWKS 검증 + 그룹 재확인 (이중 방어).

const API_BASE = process.env.MAIL_API_URL ?? "http://localhost:8080";

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

export const apiFetch = async <T>(
  idToken: string,
  path: string,
  init?: { method?: string; body?: unknown },
): Promise<T> => {
  const res = await fetch(`${API_BASE}${path}`, {
    method: init?.method ?? "GET",
    headers: {
      Authorization: `Bearer ${idToken}`,
      ...(init?.body !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    body: init?.body !== undefined ? JSON.stringify(init.body) : undefined,
  });
  if (res.status === 204) return undefined as T;
  const body = await res.json().catch(() => ({}));
  if (!res.ok) {
    throw new ApiError(res.status, (body as { error?: string }).error ?? `API ${res.status}`);
  }
  return body as T;
};

// ── DTO 타입 (Go internal/api와 동기) ───────────────────────

export type Domain = {
  id: number;
  name: string;
  active: boolean;
  createdAt: string;
  dkimSelector: string;
  dkimPublicTxt?: string;
  relayId?: number | null;
};

export type Account = {
  id: number;
  subject: string;
  email: string;
  active: boolean;
  createdAt: string;
};

export type AppPassword = {
  id: number;
  label: string;
  scopeList: string[];
  lastUsed: string | null;
  createdAt: string;
  revoked: boolean;
};

export type Address = {
  id: number;
  domainId: number;
  domainName: string;
  localPart: string; // '*' = catch-all
  accountId: number;
  accountEmail: string;
  createdAt: string;
};

export type QueueItem = {
  id: number;
  from: string;
  rcpt: string;
  status: string;
  attemptCount: number;
  nextAttemptAt: string;
  lastError: string;
  createdAt: string;
};

export type DKIMResult = { selector: string; dnsName: string; dnsTxt: string };

export type Relay = {
  id: number;
  name: string;
  host: string;
  port: number;
  username: string;
  starttls: boolean;
  isDefault: boolean;
  active: boolean;
  createdAt: string;
  hasPassword: boolean;
};

export type DnsCheck = {
  status: "ok" | "warn" | "missing";
  found: string;
  expected?: string;
  note?: string;
};

export type DnsVerify = {
  domain: string;
  mx: DnsCheck;
  spf: DnsCheck;
  dkim: DnsCheck;
  dmarc: DnsCheck;
};
