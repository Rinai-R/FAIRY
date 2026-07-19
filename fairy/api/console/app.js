const tokenInput = document.getElementById("apiToken");
const flash = document.getElementById("flash");

tokenInput.value = localStorage.getItem("fairy.apiToken") || "";
tokenInput.addEventListener("change", () => {
  localStorage.setItem("fairy.apiToken", tokenInput.value.trim());
});

function showFlash(message, isError = false) {
  flash.hidden = false;
  flash.textContent = message;
  flash.classList.toggle("error", isError);
}

async function api(path, options = {}) {
  const headers = Object.assign({ "Content-Type": "application/json" }, options.headers || {});
  const token = tokenInput.value.trim();
  if (token) headers.Authorization = `Bearer ${token}`;
  const res = await fetch(`/v1${path}`, { ...options, headers });
  const text = await res.text();
  let body = null;
  try { body = text ? JSON.parse(text) : null; } catch { body = { raw: text }; }
  if (!res.ok) {
    const msg = (body && body.error) || res.statusText || "request failed";
    throw new Error(msg);
  }
  return body;
}

function fillForm(form, data) {
  if (!data) return;
  for (const el of form.elements) {
    if (!el.name || !(el.name in data)) continue;
    if (el.type === "checkbox") el.checked = Boolean(data[el.name]);
    else el.value = data[el.name] ?? "";
  }
}

document.querySelectorAll(".tabs button").forEach((btn) => {
  btn.addEventListener("click", () => {
    document.querySelectorAll(".tabs button").forEach((b) => b.classList.remove("active"));
    document.querySelectorAll(".panel").forEach((p) => p.classList.remove("active"));
    btn.classList.add("active");
    document.getElementById(`tab-${btn.dataset.tab}`).classList.add("active");
  });
});

async function loadStatus() {
  const status = await api("/status");
  document.getElementById("statusOut").textContent = JSON.stringify(status, null, 2);
  return status;
}

async function loadModel() {
  const status = await api("/config/model");
  document.getElementById("modelStatus").textContent = JSON.stringify(status, null, 2);
  const form = document.getElementById("modelForm");
  fillForm(form, {
    protocol: status.protocol || "responses",
    endpoint: status.endpoint || "",
    model: status.model || "",
    contextWindowTokens: status.contextWindowTokens || 1048576,
    authMode: status.authMode || "bearer_key",
  });
}

async function loadSpeech() {
  const status = await api("/config/speech");
  document.getElementById("speechStatus").textContent = JSON.stringify(status, null, 2);
  fillForm(document.getElementById("speechForm"), {
    enabled: status.enabled,
    baseUrl: status.baseUrl || "",
    appId: status.appId || "",
    synthesisResourceId: status.synthesisResourceId || "",
    synthesisModel: status.synthesisModel || "",
    defaultSpeaker: status.defaultSpeaker || "",
    defaultFormat: status.defaultFormat || "mp3",
  });
}

async function loadSearch() {
  const search = await api("/config/web-search");
  document.getElementById("searchStatus").textContent = JSON.stringify(search, null, 2);
  fillForm(document.getElementById("searchForm"), {
    enabled: search.enabled,
    baseUrl: search.baseUrl || "",
  });
  const semantic = await api("/config/semantic-embedding");
  document.getElementById("semanticStatus").textContent = JSON.stringify(semantic, null, 2);
  fillForm(document.getElementById("semanticForm"), {
    provider: semantic.provider || "none",
    enabled: semantic.enabled,
    model: semantic.model || "",
  });
}

document.getElementById("refreshStatus").addEventListener("click", () => {
  loadStatus().then(() => showFlash("已刷新")).catch((e) => showFlash(e.message, true));
});

document.getElementById("modelForm").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const fd = new FormData(ev.target);
  const apiKey = String(fd.get("apiKey") || "").trim();
  const body = {
    protocol: fd.get("protocol"),
    endpoint: fd.get("endpoint"),
    model: fd.get("model"),
    contextWindowTokens: Number(fd.get("contextWindowTokens") || 0),
    authMode: fd.get("authMode"),
  };
  if (apiKey) body.apiKey = apiKey;
  try {
    const status = await api("/config/model", { method: "PUT", body: JSON.stringify(body) });
    document.getElementById("modelStatus").textContent = JSON.stringify(status, null, 2);
    ev.target.apiKey.value = "";
    showFlash("模型已保存");
  } catch (e) {
    showFlash(e.message, true);
  }
});

document.getElementById("clearModel").addEventListener("click", async () => {
  if (!confirm("清除模型连接？")) return;
  try {
    const status = await api("/config/model", { method: "DELETE" });
    document.getElementById("modelStatus").textContent = JSON.stringify(status, null, 2);
    showFlash("模型已清除");
    await loadModel();
  } catch (e) {
    showFlash(e.message, true);
  }
});

document.getElementById("speechForm").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const fd = new FormData(ev.target);
  const body = {
    enabled: fd.get("enabled") === "on",
    baseUrl: fd.get("baseUrl"),
    appId: fd.get("appId"),
    synthesisResourceId: fd.get("synthesisResourceId"),
    synthesisModel: fd.get("synthesisModel"),
    defaultSpeaker: fd.get("defaultSpeaker"),
    defaultFormat: fd.get("defaultFormat"),
    defaultLanguage: 0,
    apiKey: String(fd.get("apiKey") || ""),
    accessToken: String(fd.get("accessToken") || ""),
    clearApiKey: fd.get("clearApiKey") === "on",
    clearAccessToken: fd.get("clearAccessToken") === "on",
    trainPath: "",
    queryPath: "",
    upgradePath: "",
  };
  try {
    const status = await api("/config/speech", { method: "PUT", body: JSON.stringify(body) });
    document.getElementById("speechStatus").textContent = JSON.stringify(status, null, 2);
    ev.target.apiKey.value = "";
    ev.target.accessToken.value = "";
    showFlash("语音设置已保存");
  } catch (e) {
    showFlash(e.message, true);
  }
});

document.getElementById("clearSpeech").addEventListener("click", async () => {
  if (!confirm("清除语音设置？")) return;
  try {
    const status = await api("/config/speech", { method: "DELETE" });
    document.getElementById("speechStatus").textContent = JSON.stringify(status, null, 2);
    showFlash("语音设置已清除");
    await loadSpeech();
  } catch (e) {
    showFlash(e.message, true);
  }
});

document.getElementById("searchForm").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const fd = new FormData(ev.target);
  try {
    const status = await api("/config/web-search", {
      method: "PUT",
      body: JSON.stringify({
        enabled: fd.get("enabled") === "on",
        baseUrl: fd.get("baseUrl"),
      }),
    });
    document.getElementById("searchStatus").textContent = JSON.stringify(status, null, 2);
    showFlash("检索设置已保存");
  } catch (e) {
    showFlash(e.message, true);
  }
});

document.getElementById("semanticForm").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const fd = new FormData(ev.target);
  try {
    const status = await api("/config/semantic-embedding", {
      method: "PUT",
      body: JSON.stringify({
        schema_version: 1,
        provider: fd.get("provider"),
        enabled: fd.get("enabled") === "on",
        model: fd.get("model"),
        dimensions: 512,
      }),
    });
    document.getElementById("semanticStatus").textContent = JSON.stringify(status, null, 2);
    showFlash("语义嵌入已保存");
  } catch (e) {
    showFlash(e.message, true);
  }
});

Promise.all([loadStatus(), loadModel(), loadSpeech(), loadSearch()])
  .then(() => showFlash("控制台已就绪"))
  .catch((e) => showFlash(e.message, true));
