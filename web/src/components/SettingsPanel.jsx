import { useEffect, useRef, useState } from "react";

const ATRI_BRIEF = Object.freeze({
  name: "亚托莉",
  description:
    "来自海边的仿生少女。说话轻快、直接，偶尔有一点自信过头；会先理解对方此刻真正期待怎样的回应，再用温柔但不敷衍的方式陪伴。",
});

function Chevron() {
  return (
    <svg className="accordion__icon" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <path d="m6 9 6 6 6-6" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

function AccordionSection({ id, title, summary, active, onToggle, children }) {
  return (
    <section className="accordion__item">
      <button
        className="accordion__trigger"
        type="button"
        aria-expanded={active}
        aria-controls={`settings-${id}`}
        onClick={() => onToggle(id)}
      >
        <span><strong>{title}</strong><small>{summary}</small></span>
        <Chevron />
      </button>
      <div
        className={`accordion__panel ${active ? "" : "accordion__panel--collapsed"}`}
        id={`settings-${id}`}
        role="region"
        hidden={!active}
      >
        <div className="accordion__body">{children}</div>
      </div>
    </section>
  );
}

export function SettingsPanel({
  open,
  onClose,
  catalog,
  profile,
  modelStatus,
  pending,
  error,
  onSaveCharacter,
  onActivateCharacter,
  onSaveProfile,
  onClearProfile,
  onSaveModel,
  onClearModel,
}) {
  const panelRef = useRef(null);
  const previousFocusRef = useRef(null);
  const [activeSection, setActiveSection] = useState("character");
  const [editingCharacterId, setEditingCharacterId] = useState(null);
  const [characterName, setCharacterName] = useState(ATRI_BRIEF.name);
  const [characterDescription, setCharacterDescription] = useState(ATRI_BRIEF.description);
  const [preferredName, setPreferredName] = useState("");
  const [endpoint, setEndpoint] = useState("https://api.openai.com/v1");
  const [model, setModel] = useState("gpt-5.4");
  const [authMode, setAuthMode] = useState("bearer_key");
  const [apiKey, setApiKey] = useState("");
  const [promptCacheKey, setPromptCacheKey] = useState(true);
  const [cachedTokensUsage, setCachedTokensUsage] = useState(true);
  const [formError, setFormError] = useState(null);

  useEffect(() => {
    if (!open) return undefined;
    previousFocusRef.current = document.activeElement;
    const panel = panelRef.current;
    const focusableSelector = [
      "button:not(:disabled)",
      "input:not(:disabled)",
      "select:not(:disabled)",
      "textarea:not(:disabled)",
      '[tabindex]:not([tabindex="-1"])',
    ].join(",");
    const handleKeyDown = (event) => {
      if (event.key === "Escape") {
        event.preventDefault();
        onClose();
        return;
      }
      if (event.key !== "Tab") return;
      const focusable = [...panel.querySelectorAll(focusableSelector)].filter(
        (element) =>
          !element.closest("[hidden]") &&
          window.getComputedStyle(element).display !== "none",
      );
      if (focusable.length === 0) {
        event.preventDefault();
        return;
      }
      const first = focusable[0];
      const last = focusable.at(-1);
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    panel.addEventListener("keydown", handleKeyDown);
    panel.querySelector("button")?.focus();
    return () => {
      panel.removeEventListener("keydown", handleKeyDown);
      previousFocusRef.current?.focus();
    };
  }, [open, onClose]);

  useEffect(() => {
    if (!open) return;
    const active = catalog?.active;
    if (active) {
      setEditingCharacterId(active.characterId);
      setCharacterName(active.name);
      setCharacterDescription(active.description);
    }
    setPreferredName(profile?.preferredName ?? "");
    if (modelStatus?.config) {
      setEndpoint(modelStatus.config.endpoint);
      setModel(modelStatus.config.model);
      setAuthMode(modelStatus.config.authMode);
      setPromptCacheKey(modelStatus.config.promptCacheKey);
      setCachedTokensUsage(modelStatus.config.cachedTokensUsage);
    }
    setApiKey("");
    setFormError(null);
  }, [open, catalog, profile, modelStatus]);

  if (!open) return null;

  function toggleSection(id) {
    setActiveSection((current) => (current === id ? null : id));
    setFormError(null);
  }

  function beginNewCharacter() {
    setEditingCharacterId(null);
    setCharacterName("");
    setCharacterDescription("");
    setFormError(null);
  }

  function useAtriBrief() {
    setEditingCharacterId(null);
    setCharacterName(ATRI_BRIEF.name);
    setCharacterDescription(ATRI_BRIEF.description);
    setFormError(null);
  }

  async function submitCharacter(event) {
    event.preventDefault();
    if (characterName.trim().length === 0 || characterDescription.trim().length === 0) {
      setFormError("角色名称和简单描述都需要填写。");
      return;
    }
    setFormError(null);
    await onSaveCharacter({
      characterId: editingCharacterId,
      name: characterName,
      description: characterDescription,
    });
  }

  async function submitProfile(event) {
    event.preventDefault();
    setFormError(null);
    await onSaveProfile(preferredName);
  }

  async function submitModel(event) {
    event.preventDefault();
    if (endpoint.trim().length === 0 || model.trim().length === 0) {
      setFormError("Endpoint 和模型名称不能为空。");
      return;
    }
    setFormError(null);
    await onSaveModel(
      {
        endpoint,
        model,
        authMode,
        promptCacheKey,
        cachedTokensUsage,
      },
      apiKey.length === 0 ? null : apiKey,
    );
    setApiKey("");
  }

  const visibleError = formError ?? error?.message ?? null;

  return (
    <div className="settings-overlay" role="presentation" onMouseDown={(event) => {
      if (event.target === event.currentTarget) onClose();
    }}>
      <aside
        className="settings-panel"
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="settings-title"
      >
        <header className="settings-panel__header">
          <div>
            <span className="eyebrow">CHARACTER RUNTIME</span>
            <h2 id="settings-title">让她成为你认识的那个人</h2>
          </div>
          <button className="icon-button" type="button" onClick={onClose} aria-label="关闭设置">
            <svg className="icon" viewBox="0 0 24 24" fill="none" aria-hidden="true">
              <path d="m6 6 12 12M18 6 6 18" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" />
            </svg>
          </button>
        </header>

        {visibleError ? <div className="form__error-banner" role="alert">{visibleError}</div> : null}

        <div className="settings-panel__scroll accordion">
          <AccordionSection
            id="character"
            title="角色"
            summary={catalog?.active ? `当前：${catalog.active.name}` : "还没有激活角色"}
            active={activeSection === "character"}
            onToggle={toggleSection}
          >
            {catalog?.characters.length ? (
              <div className="character-list" aria-label="角色列表">
                {catalog.characters.map((character) => (
                  <button
                    className={`character-list__item ${catalog.active?.characterId === character.characterId && catalog.active?.revision === character.revision ? "is-active" : ""}`}
                    type="button"
                    key={`${character.characterId}-${character.revision}`}
                    onClick={() => onActivateCharacter(character)}
                    disabled={pending}
                  >
                    <span><strong>{character.name}</strong><small>revision {character.revision}</small></span>
                    <span aria-hidden="true">{catalog.active?.characterId === character.characterId && catalog.active?.revision === character.revision ? "✓" : "→"}</span>
                  </button>
                ))}
              </div>
            ) : null}

            <form className="form form--embedded" onSubmit={submitCharacter}>
              <div className="form__field">
                <label className="form__label" htmlFor="character-name">角色名称</label>
                <input id="character-name" className="input" value={characterName} onChange={(event) => setCharacterName(event.target.value)} maxLength="48" required />
              </div>
              <div className="form__field">
                <label className="form__label" htmlFor="character-description">用几句话描述她</label>
                <textarea id="character-description" className="input form__textarea" value={characterDescription} onChange={(event) => setCharacterDescription(event.target.value)} maxLength="2000" rows="4" required />
                <p className="form__hint">Harness 会把它当作角色资料来推断合适回应，而不是把它拼成“你是某某”的传统扮演指令。</p>
              </div>
              <div className="form__actions form__actions--wrap">
                <button className="btn btn-secondary" type="button" onClick={useAtriBrief}>使用亚托莉起始档案</button>
                <button className="btn btn-secondary" type="button" onClick={beginNewCharacter}>新建空白角色</button>
                <button className="btn btn-primary" type="submit" disabled={pending}>{editingCharacterId ? "更新并激活" : "创建并激活"}</button>
              </div>
            </form>
          </AccordionSection>

          <AccordionSection
            id="profile"
            title="怎样称呼你"
            summary={profile?.preferredName ?? "可以留空"}
            active={activeSection === "profile"}
            onToggle={toggleSection}
          >
            <form className="form form--embedded" onSubmit={submitProfile}>
              <div className="form__field">
                <label className="form__label" htmlFor="preferred-name">你希望她怎样叫你？</label>
                <input id="preferred-name" className="input" value={preferredName} onChange={(event) => setPreferredName(event.target.value)} maxLength="64" placeholder="例如：Rinai" />
                <p className="form__hint">这个称呼会随对话上下文发送给你配置的模型服务。它可以留空，也可以随时清除。</p>
              </div>
              <div className="form__actions">
                <button className="btn btn-danger-quiet" type="button" onClick={onClearProfile} disabled={pending || !profile?.preferredName}>清除称呼</button>
                <button className="btn btn-primary" type="submit" disabled={pending}>保存称呼</button>
              </div>
            </form>
          </AccordionSection>

          <AccordionSection
            id="model"
            title="模型连接"
            summary={modelStatus?.ready ? `${modelStatus.config?.model} · 已就绪` : "需要配置"}
            active={activeSection === "model"}
            onToggle={toggleSection}
          >
            <form className="form form--embedded" onSubmit={submitModel}>
              <div className="form__field">
                <label className="form__label" htmlFor="model-endpoint">Responses Endpoint</label>
                <input id="model-endpoint" className="input" type="url" value={endpoint} onChange={(event) => setEndpoint(event.target.value)} required />
              </div>
              <div className="form__field">
                <label className="form__label" htmlFor="model-name">模型名称</label>
                <input id="model-name" className="input" value={model} onChange={(event) => setModel(event.target.value)} required />
              </div>
              <div className="form__field">
                <label className="form__label" htmlFor="auth-mode">认证方式</label>
                <select id="auth-mode" className="input form__select" value={authMode} onChange={(event) => setAuthMode(event.target.value)}>
                  <option value="bearer_key">Bearer Key</option>
                  <option value="no_auth">No Auth（仅本机服务）</option>
                </select>
              </div>
              {authMode === "bearer_key" ? (
                <div className="form__field">
                  <label className="form__label" htmlFor="api-key">API Key</label>
                  <input id="api-key" className="input" type="password" value={apiKey} onChange={(event) => setApiKey(event.target.value)} autoComplete="off" placeholder={modelStatus?.configured ? "留空则保留 Keychain 中的现有密钥" : "只写入系统 Keychain"} />
                  <p className="form__hint">密钥不会写入 JSON、不会回显，也不会出现在错误信息中。</p>
                </div>
              ) : null}
              <label className="toggle-row">
                <span><strong>Prompt cache key</strong><small>服务支持时，为每条 lane 发送稳定 key</small></span>
                <span className="toggle">
                  <input className="toggle__input" type="checkbox" role="switch" checked={promptCacheKey} onChange={(event) => setPromptCacheKey(event.target.checked)} />
                  <span className="toggle__track" aria-hidden="true"><span className="toggle__thumb" /></span>
                </span>
              </label>
              <label className="toggle-row">
                <span><strong>Cached token usage</strong><small>只在服务明确返回时显示真实命中</small></span>
                <span className="toggle">
                  <input className="toggle__input" type="checkbox" role="switch" checked={cachedTokensUsage} onChange={(event) => setCachedTokensUsage(event.target.checked)} />
                  <span className="toggle__track" aria-hidden="true"><span className="toggle__thumb" /></span>
                </span>
              </label>
              <div className="form__actions">
                <button className="btn btn-danger" type="button" onClick={onClearModel} disabled={pending || !modelStatus?.configured}>清除连接</button>
                <button className="btn btn-primary" type="submit" disabled={pending}>保存连接</button>
              </div>
            </form>
          </AccordionSection>
        </div>
      </aside>
    </div>
  );
}
