export function buildBacklogItems(workflowHistory, workflowNodes, currentNodeID, currentLineIndex, assistantSpeaker, assistantSpeakerAliases = []) {
  const nodeByID = new Map((Array.isArray(workflowNodes) ? workflowNodes : []).map((node) => [node.id, node]));
  const speakerAliases = buildBacklogSpeakerAliases(assistantSpeaker, assistantSpeakerAliases);
  return replayAwareHistory(workflowHistory).flatMap((historyItem) => {
    if (historyItem?.action === "audio") return [];
    const nodeID = historyItem?.node_id || historyItem?.nodeID || "";
    const node = nodeByID.get(nodeID);
    const items = [];
    if (historyItem?.choice_label) {
      items.push({ kind: "user", active: false, speaker: "你", text: `选择：${cleanBacklogText(historyItem.choice_label, "你")}` });
    }
    const lines = visibleWorkflowNodeLines(node, nodeID, currentNodeID, currentLineIndex);
    if (!lines.length) {
      const rawSpeaker = node?.speaker || assistantSpeaker || "角色";
      const speaker = resolveBacklogSpeaker(rawSpeaker, assistantSpeaker, speakerAliases);
      const text = cleanBacklogText(workflowNodeDisplayText(node), speaker, [rawSpeaker, ...speakerAliases]) || historyItem?.action || "进入剧情";
      return [...items, {
        kind: "script",
        active: nodeID === currentNodeID,
        nodeID,
        node,
        speaker,
        text,
        audioURL: workflowLineAudioURL(node),
      }];
    }
    lines.forEach((line, index) => {
      const rawSpeaker = line.speaker || node?.speaker || assistantSpeaker || "角色";
      const speaker = resolveBacklogSpeaker(rawSpeaker, assistantSpeaker, speakerAliases);
      const text = cleanBacklogText(line.text, speaker, [rawSpeaker, ...speakerAliases]);
      if (!text) return;
      const isLastVisibleLine = index === lines.length - 1;
      items.push({
        kind: "script",
        active: nodeID === currentNodeID && index === currentLineIndex,
        nodeID: isLastVisibleLine ? nodeID : "",
        node: isLastVisibleLine ? node : null,
        speaker,
        text,
        audioURL: workflowLineAudioURL(line),
      });
    });
    return items;
  }).slice(-40);
}

export function workflowLineSpeechText(line) {
  return String(line?.speech_text || line?.speechText || line?.text || "").trim();
}

export function workflowLineAudioURL(line) {
  return String(line?.audio?.url || line?.audio_url || line?.audioURL || "").trim();
}

function visibleWorkflowNodeLines(node, nodeID, currentNodeID, currentLineIndex) {
  const lines = workflowNodeBacklogLines(node);
  if (nodeID !== currentNodeID) return lines;
  const visibleCount = Math.max(0, Number(currentLineIndex) || 0) + 1;
  return lines.slice(0, visibleCount);
}

function replayAwareHistory(workflowHistory) {
  return (Array.isArray(workflowHistory) ? workflowHistory : []).reduce((timeline, item) => {
    if (item?.action === "audio") return timeline;
    const nodeID = item?.node_id || item?.nodeID || "";
    if (item?.action !== "replay") return [...timeline, item];
    const existingIndex = timeline.findIndex((entry) => (entry?.node_id || entry?.nodeID || "") === nodeID);
    if (existingIndex < 0) return nodeID ? [...timeline, item] : timeline;
    return timeline.slice(0, existingIndex + 1);
  }, []);
}

function workflowNodeBacklogLines(node) {
  const lines = Array.isArray(node?.lines) ? node.lines : [];
  if (lines.length) return lines;
  const text = workflowNodeDisplayText(node);
  if (!text) return [];
  return [{ speaker: node?.speaker || "角色", text }];
}

function workflowNodeDisplayText(node) {
  const lines = Array.isArray(node?.lines) ? node.lines : [];
  if (lines.length) return lines.map((line) => String(line?.text || "").trim()).filter(Boolean).join(" ");
  return String(node?.line || "").trim();
}

function cleanBacklogSpeaker(value) {
  return String(value || "角色").replace(/[【】[\]：:]/g, "").trim() || "角色";
}

function cleanBacklogText(value, speaker, aliases = []) {
  let text = String(value || "").replace(/\s+/g, " ").trim();
  if (!text) return "";
  const names = [speaker, cleanBacklogSpeaker(speaker), ...aliases].filter(Boolean);
  for (const name of [...new Set(names)]) {
    const escaped = escapeRegExp(cleanBacklogSpeaker(name));
    text = text
      .replace(new RegExp(`(^|\\s)[【\\[]\\s*${escaped}\\s*[】\\]]\\s*`, "g"), " ")
      .replace(new RegExp(`(^|\\s)${escaped}\\s*[：:]\\s*`, "g"), " ");
  }
  return text.replace(/\s+/g, " ").trim();
}

function buildBacklogSpeakerAliases(assistantSpeaker, aliases) {
  return [...new Set([assistantSpeaker, ...(Array.isArray(aliases) ? aliases : [])].map(cleanBacklogSpeaker).filter(Boolean))];
}

function resolveBacklogSpeaker(value, assistantSpeaker, aliases) {
  const speaker = cleanBacklogSpeaker(value);
  if (aliases.includes(speaker)) return cleanBacklogSpeaker(assistantSpeaker);
  return speaker;
}

function escapeRegExp(value) {
  return String(value).replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
