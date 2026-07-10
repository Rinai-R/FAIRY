export const CONTROL_PANEL_SECTIONS = Object.freeze([
  Object.freeze({ id: "character", label: "角色" }),
  Object.freeze({ id: "profile", label: "称呼" }),
  Object.freeze({ id: "model", label: "模型连接" }),
  Object.freeze({ id: "desktop", label: "桌面行为" }),
]);

export const MODEL_PROTOCOL_OPTIONS = Object.freeze([
  Object.freeze({ value: "chat_completions", label: "Chat Completions" }),
  Object.freeze({ value: "responses", label: "Responses" }),
]);

export function assertControlPanelSection(value) {
  if (!CONTROL_PANEL_SECTIONS.some((section) => section.id === value)) {
    throw new TypeError("unsupported control panel section");
  }
  return value;
}

export function buildModelConnectionInput({ protocol, endpoint, model, authMode }) {
  if (!MODEL_PROTOCOL_OPTIONS.some((option) => option.value === protocol)) {
    throw new TypeError("unsupported model protocol");
  }
  if (authMode !== "bearer_key" && authMode !== "no_auth") {
    throw new TypeError("unsupported model auth mode");
  }
  const normalizedEndpoint = endpoint.trim();
  const normalizedModel = model.trim();
  if (normalizedEndpoint.length === 0 || normalizedModel.length === 0) {
    throw new TypeError("model base URL and model are required");
  }
  return Object.freeze({
    protocol,
    endpoint: normalizedEndpoint,
    model: normalizedModel,
    authMode,
  });
}
