// API client for the dashboard. Uses same-origin cookies (credentials:include)
// and the double-submit CSRF token (read from the lb_csrf cookie) on mutations.
// Data-plane calls pass X-App-Id for the selected app (session auth).

export interface Me {
  id: string;
  email: string;
  email_verified: boolean;
}
export interface AppInfo {
  id: string;
  name: string;
  created_at?: string;
}
export interface RankEntry {
  member: string;
  score: number;
  rank: number;
  exact: boolean;
}
export interface BoardDef {
  board: string;
  sort_order?: string;
  update_policy?: string;
  tie_break?: string;
  windows?: { kind: string }[];
}
export interface QueryOpts {
  segment?: string;
  window?: string;
}

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message);
  }
}

function csrfToken(): string {
  const m = document.cookie.match(/(?:^|; )lb_csrf=([^;]+)/);
  return m ? decodeURIComponent(m[1]) : "";
}

async function req<T>(method: string, path: string, body?: unknown, extra?: Record<string, string>): Promise<T> {
  const headers: Record<string, string> = { ...(extra ?? {}) };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (method !== "GET") headers["X-CSRF-Token"] = csrfToken();
  const resp = await fetch(path, {
    method,
    headers,
    credentials: "include",
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const text = await resp.text();
  const data = text ? JSON.parse(text) : {};
  if (!resp.ok) throw new ApiError(resp.status, (data && data.error) || resp.statusText);
  return data as T;
}

function qs(q?: QueryOpts): string {
  if (!q) return "";
  const p = new URLSearchParams();
  if (q.segment) p.set("segment", q.segment);
  if (q.window) p.set("window", q.window);
  const s = p.toString();
  return s ? "&" + s : "";
}

const appHdr = (appId: string) => ({ "X-App-Id": appId });

interface Entries {
  entries: RankEntry[];
}

export const api = {
  signup: (email: string, password: string) => req<Me>("POST", "/auth/signup", { email, password }),
  login: (email: string, password: string) => req<{ id: string; email: string; csrf_token: string }>("POST", "/auth/login", { email, password }),
  logout: () => req<unknown>("POST", "/auth/logout"),
  me: () => req<Me>("GET", "/auth/me"),
  resend: (email: string) => req<unknown>("POST", "/auth/resend", { email }),
  forgot: (email: string) => req<unknown>("POST", "/auth/forgot", { email }),
  reset: (token: string, password: string) => req<unknown>("POST", "/auth/reset", { token, password }),

  listApps: () => req<{ apps: AppInfo[] }>("GET", "/v1/apps"),
  createApp: (name: string) => req<{ id: string; name: string; api_key: string }>("POST", "/v1/apps", { name }),

  listBoards: (appId: string) => req<{ boards: { app: string; board: string }[] }>("GET", "/v1/boards", undefined, appHdr(appId)),
  createBoard: (appId: string, def: BoardDef) => req<unknown>("POST", "/v1/boards", def, appHdr(appId)),
  submit: (appId: string, board: string, member: string, score: number, segments?: string[]) =>
    req<{ accepted: boolean }>("POST", `/v1/boards/${encodeURIComponent(board)}/scores`, { member, score, segments }, appHdr(appId)),
  top: (appId: string, board: string, n: number, q?: QueryOpts) =>
    req<Entries>("GET", `/v1/boards/${encodeURIComponent(board)}/top?n=${n}${qs(q)}`, undefined, appHdr(appId)),
  rank: (appId: string, board: string, member: string, q?: QueryOpts) =>
    req<RankEntry>("GET", `/v1/boards/${encodeURIComponent(board)}/rank?member=${encodeURIComponent(member)}${qs(q)}`, undefined, appHdr(appId)),
  neighbors: (appId: string, board: string, member: string, k: number, q?: QueryOpts) =>
    req<Entries>("GET", `/v1/boards/${encodeURIComponent(board)}/neighbors?member=${encodeURIComponent(member)}&k=${k}${qs(q)}`, undefined, appHdr(appId)),
};
