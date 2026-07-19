const TOKEN_KEY = "fairy.apiToken";

export function getToken(): string {
  return localStorage.getItem(TOKEN_KEY) || "";
}

export function setToken(value: string) {
  localStorage.setItem(TOKEN_KEY, value.trim());
}

export async function api<T = unknown>(path: string, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers || {});
  if (!headers.has("Content-Type") && options.body && !(options.body instanceof FormData)) {
    headers.set("Content-Type", "application/json");
  }
  const token = getToken();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const res = await fetch(`/v1${path}`, { ...options, headers });
  const text = await res.text();
  const body: any = text ? JSON.parse(text) : null;
  if (!res.ok) {
    throw new Error((body && body.error) || res.statusText || "request failed");
  }
  return body as T;
}
