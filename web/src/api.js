function wailsApp() {
  return window.go?.desktop?.App || null;
}

const apiPrefix = "/api/v1";

async function request(path, options = {}) {
  const response = await fetch(path, {
    headers: { "content-type": "application/json", ...(options.headers || {}) },
    ...options
  });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(responseMessage(payload, "请求失败"));
  }
  return unwrapResponse(payload);
}

function unwrapResponse(payload) {
  if (payload && typeof payload === "object" && "code" in payload && "msg" in payload && "data" in payload) {
    if (payload.code !== 0) {
      throw new Error(payload.msg || "请求失败");
    }
    return payload.data;
  }
  return payload;
}

function responseMessage(payload, fallback) {
  if (typeof payload === "string") return payload || fallback;
  return payload?.msg || payload?.message || payload?.error || payload?.detail || payload?.reason || fallback;
}

export function runtimeMode() {
  return wailsApp() ? "desktop" : "web";
}

export async function getCapabilities() {
  const app = wailsApp();
  if (app) return app.Capabilities();
  return request(`${apiPrefix}/capabilities`);
}

export async function getUserConfig() {
  const app = wailsApp();
  if (app?.UserConfig) return app.UserConfig();
  return request(`${apiPrefix}/user-config`);
}

export async function saveUserConfig(config) {
  const app = wailsApp();
  if (app?.SaveUserConfig) return app.SaveUserConfig(config);
  return request(`${apiPrefix}/user-config`, {
    method: "PUT",
    body: JSON.stringify(config)
  });
}

export async function getPlugins() {
  const app = wailsApp();
  if (app) return app.Plugins();
  return request(`${apiPrefix}/plugins`);
}

export async function getProviderHealth() {
  const app = wailsApp();
  if (app?.ProviderHealth) {
    return { providers: await app.ProviderHealth() };
  }
  return request(`${apiPrefix}/providers/health`);
}

export async function getSessions() {
  const app = wailsApp();
  if (app) return { sessions: await app.Sessions() };
  return request(`${apiPrefix}/sessions`);
}

export async function getSession(id) {
  const app = wailsApp();
  if (app?.Session) return app.Session(id);
  return request(`${apiPrefix}/sessions/${encodeURIComponent(id)}`);
}

export async function deleteSession(id) {
  const app = wailsApp();
  if (app?.DeleteSession) return app.DeleteSession(id);
  return request(`${apiPrefix}/sessions/delete`, { method: "POST", body: JSON.stringify({ id }) });
}

export async function generateScene(body) {
  const app = wailsApp();
  if (app) return app.GenerateScene(body);
  return request(`${apiPrefix}/scenes/generate`, {
    method: "POST",
    body: JSON.stringify(body)
  });
}

export async function startSceneGeneration(body) {
  const app = wailsApp();
  if (app?.StartSceneGeneration) return app.StartSceneGeneration(body);
  return request(`${apiPrefix}/scenes/generate-task`, {
    method: "POST",
    body: JSON.stringify(body)
  });
}

export async function exportWebGAL(body) {
  const app = wailsApp();
  if (app?.ExportWebGAL) return app.ExportWebGAL(body);
  return request(`${apiPrefix}/webgal/export`, {
    method: "POST",
    body: JSON.stringify(body)
  });
}

export async function advanceWorkflow(body) {
  const app = wailsApp();
  if (app?.AdvanceWorkflow) return app.AdvanceWorkflow(body);
  return request(`${apiPrefix}/workflows/advance`, {
    method: "POST",
    body: JSON.stringify(body)
  });
}

export async function uploadDocumentAsset(file) {
  const app = wailsApp();
  if (app?.StoreDocumentAsset) return app.StoreDocumentAsset(await documentUploadPayload(file));

  const form = new FormData();
  form.append("file", file);
  const response = await fetch(`${apiPrefix}/documents/upload`, {
    method: "POST",
    body: form
  });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(responseMessage(payload, "文件上传失败"));
  }
  return unwrapResponse(payload);
}

export async function turn(body) {
  const app = wailsApp();
  if (app) return app.Turn(body);
  return request(`${apiPrefix}/turn`, {
    method: "POST",
    body: JSON.stringify(body)
  });
}

export async function synthesizeVoice(body) {
  const app = wailsApp();
  if (app?.SynthesizeVoice) return app.SynthesizeVoice(body);
  return request(`${apiPrefix}/voices/synthesize`, {
    method: "POST",
    body: JSON.stringify(body)
  });
}

export async function cloneVoice(body) {
  const app = wailsApp();
  if (app?.CloneVoice) return app.CloneVoice(body);
  return request(`${apiPrefix}/voices/clone`, {
    method: "POST",
    body: JSON.stringify(body)
  });
}

export async function cloneVoiceStatus(body) {
  const app = wailsApp();
  if (app?.CloneVoiceStatus) return app.CloneVoiceStatus(body);
  return request(`${apiPrefix}/voices/clone/status`, {
    method: "POST",
    body: JSON.stringify(body)
  });
}

export async function streamTurn(body, onToken) {
  const app = wailsApp();
  if (app) {
    const payload = await app.Turn(body);
    onToken?.(payload.display_text || "");
    return payload;
  }

  const response = await fetch(`${apiPrefix}/turn/stream`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body)
  });
  if (!response.ok || !response.body) {
    const payload = await response.json().catch(() => ({}));
    throw new Error(responseMessage(payload, "流式请求失败"));
  }

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  let donePayload = null;
  while (true) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    const parts = buffer.split("\n\n");
    buffer = parts.pop() || "";
    for (const part of parts) {
      const event = parseSSE(part);
      if (event.type === "token") {
        onToken?.(event.data.text || "");
      }
      if (event.type === "done") {
        donePayload = event.data;
      }
    }
  }
  return donePayload;
}

function parseSSE(chunk) {
  const lines = chunk.split("\n");
  const type = (lines.find((line) => line.startsWith("event:")) || "").slice(6).trim();
  const data = (lines.find((line) => line.startsWith("data:")) || "").slice(5).trim();
  return { type, data: data ? JSON.parse(data) : {} };
}

async function documentUploadPayload(file) {
  const dataURL = await new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result || ""));
    reader.onerror = () => reject(reader.error || new Error("读取文件失败"));
    reader.readAsDataURL(file);
  });
  const [, dataBase64 = ""] = dataURL.split(",");
  return {
    filename: file.name,
    content_type: file.type || "application/octet-stream",
    data_base64: dataBase64
  };
}
