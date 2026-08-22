export function resolveChatKeyboardAction(key, shiftKey) {
  if (typeof key !== "string" || typeof shiftKey !== "boolean") {
    throw new TypeError("chat keyboard input is invalid");
  }
  if (key === "Escape") return "close";
  if (key === "Enter" && !shiftKey) return "submit";
  return "none";
}
