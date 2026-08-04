const TOKEN_KEY = "fairy.apiToken";
export const API_UNAUTHORIZED_EVENT = "fairy:api-unauthorized";
let credentialRevision = 0;

export class ApiError extends Error {
  readonly status: number;

  constructor(message: string, status: number) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

export function getToken(): string {
  return localStorage.getItem(TOKEN_KEY) || "";
}

export function setToken(value: string) {
  const token = value.trim();
  if (token) {
    localStorage.setItem(TOKEN_KEY, token);
    credentialRevision += 1;
    return;
  }
  localStorage.removeItem(TOKEN_KEY);
  credentialRevision += 1;
}

function notifyUnauthorized(requestRevision: number) {
  if (requestRevision === credentialRevision && typeof window !== "undefined") {
    window.dispatchEvent(new Event(API_UNAUTHORIZED_EVENT));
  }
}

function requestHeaders(options: RequestInit, token: string): Headers {
  const headers = new Headers(options.headers || {});
  if (!headers.has("Content-Type") && options.body && !(options.body instanceof FormData)) {
    headers.set("Content-Type", "application/json");
  }
  if (token) headers.set("Authorization", `Bearer ${token}`);
  return headers;
}

async function requestJSON<T>(path: string, token: string, options: RequestInit, requestRevision: number): Promise<T> {
  const headers = requestHeaders(options, token);
  const res = await fetch(`/v1${path}`, { ...options, headers });
  if (res.status === 401) notifyUnauthorized(requestRevision);
  const text = await res.text();
  let body: any = null;
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      if (res.ok) throw new ApiError("服务返回了无效 JSON", res.status);
    }
  }
  if (!res.ok) {
    throw new ApiError((body && body.error) || res.statusText || "request failed", res.status);
  }
  return body as T;
}

export async function api<T = unknown>(path: string, options: RequestInit = {}): Promise<T> {
  return requestJSON<T>(path, getToken(), options, credentialRevision);
}

export async function apiWithToken<T = unknown>(path: string, token: string, options: RequestInit = {}): Promise<T> {
  return requestJSON<T>(path, token.trim(), options, credentialRevision);
}

export async function apiBlob(path: string): Promise<Blob> {
  const requestRevision = credentialRevision;
  const headers = new Headers();
  const token = getToken();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const res = await fetch(`/v1${path}`, { headers });
  if (res.status === 401) notifyUnauthorized(requestRevision);
  if (!res.ok) {
    const text = await res.text();
    let message = res.statusText || "request failed";
    try {
      message = JSON.parse(text)?.error || message;
    } catch {
      // Non-JSON image errors keep the bounded HTTP status text.
    }
    throw new ApiError(message, res.status);
  }
  return res.blob();
}
