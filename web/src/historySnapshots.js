const CHAPTER_KINDS = new Set(["opening", "lesson", "summary"]);

export function chapterSnapshotItems(nodes, options = {}) {
  const source = chapterSnapshotNodes(nodes);
  const limit = normalizeSnapshotLimit(options.limit, source.length);
  return source.slice(0, limit).map((node, index) => ({
    key: node?.id || `chapter-${index + 1}`,
    number: index + 1,
    node
  }));
}

export function hiddenChapterCount(nodes, options = {}) {
  const source = chapterSnapshotNodes(nodes);
  const limit = normalizeSnapshotLimit(options.limit, source.length);
  return Math.max(0, source.length - limit);
}

export function chapterSnapshotCount(nodes) {
  return chapterSnapshotNodes(nodes).length;
}

export function chapterSnapshotPosition(nodes, nodeOrID) {
  const nodeID = typeof nodeOrID === "object" ? nodeOrID?.id : nodeOrID;
  const normalizedID = String(nodeID || "").trim();
  if (!normalizedID) return null;
  const source = chapterSnapshotNodes(nodes);
  const index = source.findIndex((node) => String(node?.id || "").trim() === normalizedID);
  if (index < 0) return null;
  return {
    index,
    number: index + 1,
    total: source.length,
    node: source[index]
  };
}

export function visitedChapterCount(nodes, history) {
  const chapterIDs = new Set(chapterSnapshotNodes(nodes).map((node) => String(node?.id || "").trim()).filter(Boolean));
  if (!chapterIDs.size) return 0;
  const visited = new Set();
  for (const item of Array.isArray(history) ? history : []) {
    const action = String(item?.action || "").trim();
    if (action === "audio") continue;
    const nodeID = String(item?.node_id || item?.id || "").trim();
    if (chapterIDs.has(nodeID)) visited.add(nodeID);
  }
  return visited.size;
}

export function chapterSnapshotNodes(nodes) {
  return (Array.isArray(nodes) ? nodes : []).filter(isChapterSnapshotNode);
}

function isChapterSnapshotNode(node) {
  if (!node || typeof node !== "object") return false;
  const kind = String(node.kind || "").trim();
  return CHAPTER_KINDS.has(kind);
}

function normalizeSnapshotLimit(value, fallback) {
  if (value === undefined || value === null || value === Infinity) return fallback;
  const limit = Number(value);
  if (!Number.isFinite(limit) || limit <= 0) return fallback;
  return Math.trunc(limit);
}
