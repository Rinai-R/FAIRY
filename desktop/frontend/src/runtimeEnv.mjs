/** Shared runtime detection for Wails vs browser preview. */
export function isWailsRuntime() {
  if (typeof window === "undefined") return false;
  if (window._wails) return true;
  const host = window.location?.hostname ?? "";
  if (host === "wails.localhost" || host.endsWith(".wails.localhost") || host === "wails") {
    return true;
  }
  const protocol = window.location?.protocol ?? "";
  if (protocol === "wails:" || protocol === "wails-webview:") {
    return true;
  }
  return false;
}

/** Await runtime injection so first-paint gates do not race window._wails. */
export async function ensureWailsRuntimeReady() {
  if (typeof window === "undefined") return false;
  try {
    await import("@wailsio/runtime");
  } catch {
    // Browser preview may not ship the Wails runtime module.
  }
  return isWailsRuntime();
}
