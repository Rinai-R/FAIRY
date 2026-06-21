import { spawn } from "node:child_process";
import { mkdir, rm, writeFile } from "node:fs/promises";
import net from "node:net";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { defaultCharacters } from "../src/defaultCharacters.js";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const webRoot = path.resolve(scriptDir, "..");
const repoRoot = path.resolve(webRoot, "..");

const outDir = path.resolve(process.env.FAIRY_VISUAL_OUT || "/tmp/fairy-visual-smoke");
const screensDir = path.join(outDir, "screens");
const dataDir = path.join(outDir, "data");
const chromeProfileDir = path.join(outDir, "chrome-profile");
const apiPort = await resolvePort("FAIRY_VISUAL_API_PORT", 8787);
const webPort = await resolvePort("FAIRY_VISUAL_WEB_PORT", 5177);
const cdpPort = await resolvePort("FAIRY_VISUAL_CDP_PORT", 9224);
const apiOrigin = `http://127.0.0.1:${apiPort}`;
const webOrigin = `http://127.0.0.1:${webPort}`;
const chromeBin = process.env.CHROME_BIN || "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
const keepServers = process.env.FAIRY_VISUAL_KEEP_SERVERS === "1";

const childProcesses = [];

async function main() {
  try {
    await prepareFixture();

    const api = spawnLogged("api", "rtk", ["go", "run", "./cmd/fairy"], {
      cwd: repoRoot,
      env: {
        ...process.env,
        FAIRY_ADDR: `127.0.0.1:${apiPort}`,
        FAIRY_SESSION_PATH: path.join(dataDir, "sessions.json"),
        FAIRY_USER_CONFIG_PATH: path.join(dataDir, "user-config.json"),
        FAIRY_AUDIO_DIR: path.join(outDir, "audio"),
        FAIRY_IMAGE_DIR: path.join(outDir, "images"),
        FAIRY_MATERIAL_DIR: path.join(outDir, "materials"),
        FAIRY_AUDIO_BASE_URL: "/audio/",
        FAIRY_IMAGE_BASE_URL: "/images/",
      },
    });
    childProcesses.push(api);
    await waitForHTTP(`${apiOrigin}/api/v1/sessions`, "FAIRY API");

    const vite = spawnLogged("vite", "rtk", ["npm", "--prefix", "web", "run", "dev", "--", "--port", String(webPort)], {
      cwd: repoRoot,
      env: {
        ...process.env,
        FAIRY_API_PROXY: apiOrigin,
      },
    });
    childProcesses.push(vite);
    await waitForHTTP(webOrigin, "Vite dev server");

    const chrome = spawnLogged("chrome", chromeBin, [
      "--headless=new",
      `--remote-debugging-port=${cdpPort}`,
      `--user-data-dir=${chromeProfileDir}`,
      "--disable-gpu",
      "--no-first-run",
      "about:blank",
    ], { cwd: repoRoot, env: process.env });
    childProcesses.push(chrome);
    await waitForHTTP(`http://127.0.0.1:${cdpPort}/json/version`, "Chrome CDP");

    const report = {
      generated_at: new Date().toISOString(),
      web_origin: webOrigin,
      api_origin: apiOrigin,
      out_dir: outDir,
      viewports: [],
    };

    const desktop = await openCDPPage(cdpPort, { width: 1440, height: 952, deviceScaleFactor: 1, mobile: false });
    try {
      report.viewports.push(await runViewportSmoke(desktop, "desktop", { width: 1440, height: 952, failOnOverflow: false }));
    } finally {
      desktop.close();
    }

    const tablet = await openCDPPage(cdpPort, { width: 1024, height: 768, deviceScaleFactor: 1, mobile: false });
    try {
      report.viewports.push(await runViewportSmoke(tablet, "tablet", { width: 1024, height: 768, failOnOverflow: true }));
    } finally {
      tablet.close();
    }

    const mobile = await openCDPPage(cdpPort, { width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
    try {
      report.viewports.push(await runViewportSmoke(mobile, "mobile", { width: 390, height: 844, failOnOverflow: true }));
    } finally {
      mobile.close();
    }

    const reportPath = path.join(outDir, "visual-smoke-report.json");
    await writeFile(reportPath, `${JSON.stringify(report, null, 2)}\n`);

    const failures = report.viewports.flatMap((viewport) => viewport.checks.filter((check) => !check.pass)
      .map((check) => `${viewport.name}:${check.name} ${check.reason}`));
    if (failures.length) {
      throw new Error(`视觉冒烟失败:\n${failures.join("\n")}\n报告: ${reportPath}`);
    }

    console.log(`visual smoke passed: ${reportPath}`);
  } finally {
    if (!keepServers) {
      await stopChildren(childProcesses);
    } else {
      console.log("FAIRY_VISUAL_KEEP_SERVERS=1，保留临时服务进程。");
    }
  }
}

async function prepareFixture() {
  await rm(outDir, { recursive: true, force: true });
  await mkdir(screensDir, { recursive: true });
  await mkdir(dataDir, { recursive: true });
  await mkdir(chromeProfileDir, { recursive: true });
  await mkdir(path.join(outDir, "audio"), { recursive: true });
  await mkdir(path.join(outDir, "images"), { recursive: true });
  await mkdir(path.join(outDir, "materials"), { recursive: true });

  const sessions = buildSessionFixture();
  await writeFile(path.join(dataDir, "sessions.json"), `${JSON.stringify(sessions, null, 2)}\n`);
  await writeFile(path.join(dataDir, "user-config.json"), `${JSON.stringify(buildUserConfig(), null, 2)}\n`);
}

function buildUserConfig() {
  return {
    documentTitle: "Go 调度器",
    documentSourceMode: "text",
    documentAsset: null,
    learningGoal: "理解 G、M、P 的协作关系",
    activeCharacterID: "tutor",
    providerConfig: {
      agentProvider: "mock",
      voiceProvider: "volcengine",
      imageProvider: "mock",
      sceneProvider: "mock",
    },
  };
}

function buildSessionFixture() {
  const tutor = JSON.parse(JSON.stringify(defaultCharacters.find((item) => item.id === "tutor") || defaultCharacters[0]));
  const playable = buildRecord({
    id: "visual-smoke-playable",
    title: "Go 调度器的魔法课堂",
    subtitle: "理解 G、M、P 的协作关系",
    status: "preparing",
    updatedAt: "2026-06-20T09:20:00.000Z",
    workflow: {
      current_node_id: "opening",
      preparing: true,
      pending_node_id: "lesson-1",
      nodes: [
        readyNode("opening", "opening", "开场对白", [
          line("亚托莉", "欢迎来到 Go 调度器的魔法课堂～", "soft_smile", "/audio/opening-1.mp3"),
          line("亚托莉", "我们会用小组作业来理解 G、M、P。", "curious", "/audio/opening-2.mp3"),
        ], { next_node_id: "lesson-1", ready_at: "2026-06-20T09:18:00.000Z" }),
        pendingNode("lesson-1", "lesson", "G、M、P 分工"),
      ],
      history: [
        historyItem("opening", "开场对白", "opening", "advance", "2026-06-20T09:18:00.000Z", "/audio/opening-1.mp3"),
      ],
    },
    events: [
      event("visual-event-created", "info", "generation.created", "generation", "生成任务已创建", { created_at: "2026-06-20T09:16:00.000Z" }),
      event("visual-event-open", "info", "workflow.node.completed", "workflow", "开场对白已就绪", {
        node_id: "opening",
        duration_ms: 620,
        created_at: "2026-06-20T09:18:00.000Z",
      }),
    ],
    character: tutor,
  });

  const completed = buildRecord({
    id: "visual-smoke-completed",
    title: "调度器完整演示",
    subtitle: "从开场到自由讨论闭合",
    status: "ready",
    updatedAt: "2026-06-20T09:10:00.000Z",
    workflow: {
      current_node_id: "free-discussion",
      nodes: [
        readyNode("opening", "opening", "开场对白", [line("亚托莉", "我们先把调度器看成一个教室。", "soft_smile", "/audio/completed-opening.mp3")], {
          next_node_id: "lesson-1",
          ready_at: "2026-06-20T09:01:00.000Z",
        }),
        readyNode("lesson-1", "lesson", "G、M、P 分工", [
          line("亚托莉", "G 是待执行的 goroutine，像任务卡片。", "thinking", "/audio/completed-lesson.mp3"),
          line("亚托莉", "M 是真正执行任务的线程。", "serious", "/audio/completed-lesson-2.mp3"),
        ], {
          next_node_id: "free-discussion",
          ready_at: "2026-06-20T09:04:00.000Z",
          choices: [
            { id: "choice-g", label: "G", text: "G 像待办事项卡片", target_node_id: "free-discussion" },
            { id: "choice-m", label: "M", text: "M 像真正动手的人", target_node_id: "free-discussion" },
          ],
        }),
        readyNode("free-discussion", "free_discussion", "自由讨论", [line("亚托莉", "现在你可以自由追问调度器里的细节。", "curious", "/audio/completed-free.mp3")], {
          free_discussion: true,
          ready_at: "2026-06-20T09:06:00.000Z",
        }),
      ],
      history: [
        historyItem("opening", "开场对白", "opening", "advance", "2026-06-20T09:01:00.000Z", "/audio/completed-opening.mp3"),
        historyItem("lesson-1", "G、M、P 分工", "lesson", "advance", "2026-06-20T09:04:00.000Z", "/audio/completed-lesson.mp3"),
        historyItem("free-discussion", "自由讨论", "free_discussion", "advance", "2026-06-20T09:06:00.000Z", "/audio/completed-free.mp3"),
      ],
    },
    events: [
      event("visual-event-complete", "info", "generation.completed", "generation", "全部章节已生成", { created_at: "2026-06-20T09:06:00.000Z" }),
    ],
    character: tutor,
  });

  const failed = buildRecord({
    id: "visual-smoke-failed",
    title: "语音失败样例",
    subtitle: "火山并发超过限制",
    status: "failed",
    error: "volcengine v3 tts 解析失败: quota exceeded for types: concurrency",
    updatedAt: "2026-06-20T09:00:00.000Z",
    workflow: {
      current_node_id: "lesson-1",
      nodes: [
        readyNode("opening", "opening", "开场对白", [line("亚托莉", "这条记录用来验证错误观测。", "serious", "/audio/failed-opening.mp3")], {
          next_node_id: "lesson-1",
          ready_at: "2026-06-20T08:58:00.000Z",
        }),
        {
          id: "lesson-1",
          kind: "lesson",
          title: "语音失败",
          speaker: "亚托莉",
          status: "error",
          voice_status: "error",
          prepare_error: "quota exceeded for types: concurrency",
          lines: [
            {
              speaker: "亚托莉",
              text: "这句台词触发了语音并发限制。",
              expression: "serious",
              audio_status: "error",
              audio_error: "quota exceeded for types: concurrency",
            },
          ],
        },
      ],
      history: [
        historyItem("opening", "开场对白", "opening", "advance", "2026-06-20T08:58:00.000Z", "/audio/failed-opening.mp3"),
      ],
    },
    events: [
      event("visual-event-voice-failed", "error", "voice.synthesize.failed", "voice", "quota exceeded for types: concurrency", {
        node_id: "lesson-1",
        provider: "volcengine",
        retry_count: 2,
        duration_ms: 1480,
        created_at: "2026-06-20T08:59:00.000Z",
      }),
      event("visual-event-agent-retry", "warn", "agent.generate_act.retry", "agent", "Agent 输出不符合合约，正在修正重试。", {
        node_id: "lesson-1",
        provider: "fairy-agent",
        retry_count: 1,
        duration_ms: 820,
        detail: "node.lines[0].text 包含非屏幕显示语言",
        created_at: "2026-06-20T08:58:30.000Z",
      }),
    ],
    character: tutor,
  });

  return {
    [playable.session.id]: playable,
    [completed.session.id]: completed,
    [failed.session.id]: failed,
  };
}

function buildRecord({ id, title, subtitle, status, error = "", updatedAt, workflow, events, character }) {
  const startedAt = "2026-06-20T08:55:00.000Z";
  const completedAt = status === "ready" || status === "failed" ? updatedAt : "";
  const generation = {
    status,
    fingerprint: `${id}-fingerprint`,
    error,
    started_at: startedAt,
    request: {
      topic: title,
      learning_goal: subtitle,
      material_source: {
        mode: "text",
        text: "G 是 goroutine，M 是系统线程，P 是执行上下文。",
      },
      characters: [character],
    },
  };
  if (completedAt) generation.completed_at = completedAt;

  return {
    session: {
      id,
      user_id: "default",
      active_character_id: "tutor",
      participant_ids: ["tutor"],
    },
    scene: {
      id: "lesson",
      title,
      location: "FAIRY 教室",
      phase: "opening",
      variables: {
        background_url: character.assets?.background_url || "",
      },
      last_active_at: updatedAt,
    },
    teaching: {
      topic: title,
      learning_goal: subtitle,
      material_source: {
        mode: "text",
        text: "G 是 goroutine，M 是系统线程，P 是执行上下文。调度器负责把可运行的 G 安排给 M 执行。",
      },
      material_context: {
        brief: "Go 调度器 GMP 课堂材料",
        text: "G 是 goroutine，M 是系统线程，P 是执行上下文。调度器负责把可运行的 G 安排给 M 执行。",
        report: {
          mode: "text",
          summary: "GMP 调度关系摘要",
          total_bytes: 96,
          truncated: false,
        },
      },
    },
    characters: [character],
    interaction: { mode: "galgame" },
    workflow: {
      id: `${id}-workflow`,
      title,
      goal: subtitle,
      ...workflow,
    },
    relation: {
      user_id: "default",
      affinity: 0.2,
      trust: 0.4,
      tension: 0.1,
      closeness: 0.3,
      updated_at: updatedAt,
    },
    messages: [],
    generation,
    events: events.map((item) => ({ ...item, session_id: id })),
    updated_at: updatedAt,
  };
}

function readyNode(id, kind, title, lines, extra = {}) {
  return {
    id,
    kind,
    title,
    speaker: "亚托莉",
    status: "ready",
    voice_status: "ready",
    lines,
    ...extra,
  };
}

function pendingNode(id, kind, title) {
  return {
    id,
    kind,
    title,
    speaker: "亚托莉",
    status: "pending",
    voice_status: "pending",
    lines: [],
  };
}

function line(speaker, text, expression, audioURL) {
  return {
    speaker,
    text,
    speech_text: text,
    expression,
    audio: {
      url: audioURL,
      format: "mp3",
      duration_ms: 1280,
      cached: false,
    },
    audio_status: "ready",
  };
}

function historyItem(nodeID, nodeTitle, nodeKind, action, occurredAt, audioURL) {
  return {
    node_id: nodeID,
    node_title: nodeTitle,
    node_kind: nodeKind,
    action,
    audio_url: audioURL,
    audio_format: "mp3",
    audio_cached: false,
    occurred_at: occurredAt,
  };
}

function event(id, level, type, stage, message, extra = {}) {
  return {
    id,
    session_id: "",
    level,
    type,
    stage,
    message,
    detail: extra.detail || "",
    node_id: extra.node_id || "",
    provider: extra.provider || "",
    retry_count: extra.retry_count || 0,
    duration_ms: extra.duration_ms || 0,
    created_at: extra.created_at || "2026-06-20T08:55:00.000Z",
  };
}

async function runViewportSmoke(page, name, options) {
  const checks = [];
  const captures = [];

  await navigate(page, `${webOrigin}/#history`, ".hist2");
  captures.push(await capture(page, `${name}-history.png`));
  checks.push(checkLayout(name, "history", await layoutReport(page), options.failOnOverflow));

  const deleteClicked = await evaluate(page, clickButtonExpression("删除记录"));
  if (!deleteClicked) throw new Error(`${name}: 未找到删除记录按钮`);
  await waitForSelector(page, ".h-delete-confirm");
  captures.push(await capture(page, `${name}-history-delete.png`));
  checks.push(checkLayout(name, "history-delete", await layoutReport(page), options.failOnOverflow));

  await navigate(page, `${webOrigin}/#logs`, ".log-console-shell");
  captures.push(await capture(page, `${name}-logs.png`));
  checks.push(checkLayout(name, "logs", await layoutReport(page), options.failOnOverflow));

  await navigate(page, `${webOrigin}/#history`, ".hist2");
  const entered = await evaluate(page, clickButtonExpression("进入演出"));
  if (!entered) throw new Error(`${name}: 未找到进入演出按钮`);
  await waitForSelector(page, ".vn-stage-frame");
  await wait(600);
  captures.push(await capture(page, `${name}-vn-stage.png`));
  const stageLayout = await layoutReport(page);
  checks.push(checkLayout(name, "vn-stage", stageLayout, options.failOnOverflow));
  checks.push(checkCharacterVisible(name, stageLayout));

  return { name, viewport: options, captures, checks };
}

function checkLayout(viewport, name, report, failOnOverflow) {
  const overflow = report.scrollWidth > report.innerWidth + 2 || report.overflow.length > 0;
  return {
    name,
    pass: !failOnOverflow || !overflow,
    reason: overflow ? `scrollWidth=${report.scrollWidth}, innerWidth=${report.innerWidth}, overflow=${report.overflow.length}` : "",
    report,
  };
}

function checkCharacterVisible(viewport, report) {
  const character = report.elements.find((item) => item.selector === ".vn-character-sprite");
  const pass = Boolean(character && character.width >= 80 && character.height >= 120 && character.bottom > 120);
  return {
    name: "vn-character-visible",
    pass,
    reason: pass ? "" : `${viewport}: VN 立绘不可见或尺寸异常`,
    report: character || null,
  };
}

async function layoutReport(page) {
  return evaluate(page, `(() => {
    const selectors = [
      ".fairy-window",
      ".fairy-titlebar",
      ".fairy-shell",
      ".side-nav",
      ".workspace",
      ".page-header",
      ".hist2",
      ".hist2-stage",
      ".hist2-insp",
      ".h-table",
      ".h-table .row.item",
      ".h-delete-confirm",
      ".log-console-shell",
      ".log-toolbar",
      ".log-table",
      ".log-table__row:not(.log-table__row--head)",
      ".vn-stage-frame",
      ".vn-character-sprite",
      ".vn-dialogue-box",
      ".vn-dialogue-speaker",
      ".vn-dialogue-text",
      ".vn-dialogue-actions",
      ".vn-choice-layer"
    ];
    const innerWidth = window.innerWidth;
    const innerHeight = window.innerHeight;
    const elements = selectors.flatMap((selector) => Array.from(document.querySelectorAll(selector)).map((el) => {
      const rect = el.getBoundingClientRect();
      return {
        selector,
        left: Math.round(rect.left),
        right: Math.round(rect.right),
        top: Math.round(rect.top),
        bottom: Math.round(rect.bottom),
        width: Math.round(rect.width),
        height: Math.round(rect.height),
        text: (el.textContent || "").trim().slice(0, 80)
      };
    })).filter((item) => item.width > 0 && item.height > 0);
    const overflow = elements.filter((item) => item.right > 0 && item.left < innerWidth && (item.left < -2 || item.right > innerWidth + 2));
    return {
      innerWidth,
      innerHeight,
      scrollWidth: Math.max(document.documentElement.scrollWidth, document.body?.scrollWidth || 0),
      scrollHeight: Math.max(document.documentElement.scrollHeight, document.body?.scrollHeight || 0),
      elements,
      overflow
    };
  })()`);
}

async function navigate(page, url, selector) {
  await page.send("Page.navigate", { url });
  await waitForSelector(page, selector);
  await wait(500);
}

async function waitForSelector(page, selector, timeoutMs = 12000) {
  const started = Date.now();
  while (Date.now() - started < timeoutMs) {
    const found = await evaluate(page, `Boolean(document.querySelector(${JSON.stringify(selector)}))`);
    if (found) return;
    await wait(100);
  }
  throw new Error(`等待元素超时: ${selector}`);
}

async function capture(page, filename) {
  const result = await page.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: false });
  const filePath = path.join(screensDir, filename);
  await writeFile(filePath, Buffer.from(result.data, "base64"));
  return filePath;
}

function clickButtonExpression(text) {
  return `(() => {
    const target = Array.from(document.querySelectorAll("button")).find((button) => (button.textContent || "").includes(${JSON.stringify(text)}) && !button.disabled);
    if (!target) return false;
    target.click();
    return true;
  })()`;
}

async function openCDPPage(port, viewport) {
  const target = await fetchJSON(`http://127.0.0.1:${port}/json/new?${encodeURIComponent(`${webOrigin}/#history`)}`, {
    method: "PUT",
  });
  const page = new CDPPage(target.webSocketDebuggerUrl);
  await page.open();
  await page.send("Page.enable");
  await page.send("Runtime.enable");
  await page.send("Emulation.setDeviceMetricsOverride", viewport);
  return page;
}

async function evaluate(page, expression) {
  const result = await page.send("Runtime.evaluate", {
    expression,
    awaitPromise: true,
    returnByValue: true,
  });
  if (result.exceptionDetails) {
    throw new Error(result.exceptionDetails.text || "Runtime.evaluate failed");
  }
  return result.result?.value;
}

class CDPPage {
  constructor(url) {
    this.url = url;
    this.socket = null;
    this.nextID = 1;
    this.pending = new Map();
  }

  async open() {
    this.socket = new WebSocket(this.url);
    this.socket.addEventListener("message", (event) => {
      const message = JSON.parse(event.data);
      if (!message.id) return;
      const pending = this.pending.get(message.id);
      if (!pending) return;
      this.pending.delete(message.id);
      if (message.error) pending.reject(new Error(message.error.message || "CDP error"));
      else pending.resolve(message.result || {});
    });
    await new Promise((resolve, reject) => {
      this.socket.addEventListener("open", resolve, { once: true });
      this.socket.addEventListener("error", reject, { once: true });
    });
  }

  send(method, params = {}) {
    const id = this.nextID++;
    const payload = JSON.stringify({ id, method, params });
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.socket.send(payload);
      setTimeout(() => {
        if (!this.pending.has(id)) return;
        this.pending.delete(id);
        reject(new Error(`CDP timeout: ${method}`));
      }, 15000);
    });
  }

  close() {
    try {
      this.socket?.close();
    } catch {
      // best effort cleanup
    }
  }
}

function spawnLogged(label, command, args, options) {
  const child = spawn(command, args, {
    cwd: options.cwd,
    env: options.env,
    detached: true,
    stdio: ["ignore", "pipe", "pipe"],
  });
  child.label = label;
  child.recentOutput = [];
  child.stdout.on("data", (data) => collectOutput(child, data));
  child.stderr.on("data", (data) => collectOutput(child, data));
  child.on("error", (err) => {
    collectOutput(child, Buffer.from(`${err.message}\n`));
  });
  return child;
}

function collectOutput(child, data) {
  const lines = String(data).split(/\r?\n/).filter(Boolean);
  child.recentOutput.push(...lines.map((line) => `[${child.label}] ${line}`));
  if (child.recentOutput.length > 80) child.recentOutput.splice(0, child.recentOutput.length - 80);
}

async function stopChildren(children) {
  for (const child of children.reverse()) {
    if (!child.pid || child.exitCode !== null) continue;
    try {
      process.kill(-child.pid, "SIGTERM");
    } catch {
      try {
        child.kill("SIGTERM");
      } catch {
        // best effort cleanup
      }
    }
  }
  await wait(600);
}

async function waitForHTTP(url, label, timeoutMs = 30000) {
  const started = Date.now();
  let lastError = null;
  while (Date.now() - started < timeoutMs) {
    try {
      const response = await fetch(url);
      if (response.ok) return;
      lastError = new Error(`${response.status} ${response.statusText}`);
    } catch (err) {
      lastError = err;
    }
    await wait(250);
  }
  const logs = childProcesses.flatMap((child) => child.recentOutput || []).join("\n");
  throw new Error(`${label} 未启动: ${lastError?.message || "timeout"}\n${logs}`);
}

async function fetchJSON(url, options = {}) {
  const response = await fetch(url, options);
  if (!response.ok) throw new Error(`${url} failed: ${response.status} ${response.statusText}`);
  return response.json();
}

async function resolvePort(envName, fallback) {
  const configured = process.env[envName];
  if (configured) {
    const port = Number(configured);
    if (!Number.isInteger(port) || port <= 0) throw new Error(`${envName} 必须是有效端口: ${configured}`);
    await assertPortAvailable(port, envName);
    return port;
  }
  return findAvailablePort(fallback);
}

async function findAvailablePort(start) {
  for (let port = start; port < start + 100; port += 1) {
    if (await isPortAvailable(port)) return port;
  }
  throw new Error(`没有找到可用端口: ${start}-${start + 99}`);
}

async function assertPortAvailable(port, label) {
  if (await isPortAvailable(port)) return;
  throw new Error(`${label}=${port} 已被占用`);
}

function isPortAvailable(port) {
  return new Promise((resolve) => {
    const server = net.createServer();
    server.once("error", () => resolve(false));
    server.once("listening", () => {
      server.close(() => resolve(true));
    });
    server.listen(port, "127.0.0.1");
  });
}

function wait(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

await main();
