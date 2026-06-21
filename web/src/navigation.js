export const APP_VIEWS = Object.freeze(["dashboard", "history", "director", "config", "logs", "stage"]);

const VIEW_SET = new Set(APP_VIEWS);

export function normalizeAppView(value) {
  const view = String(value || "").trim().replace(/^#/, "");
  return VIEW_SET.has(view) ? view : "dashboard";
}

export function viewFromLocationHash(hash) {
  return normalizeAppView(hash);
}

export function hashForView(view) {
  return `#${normalizeAppView(view)}`;
}
