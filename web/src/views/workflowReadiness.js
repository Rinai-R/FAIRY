export function stageWorkflowWaiting(workflow) {
  const nodes = Array.isArray(workflow?.nodes) ? workflow.nodes : [];
  const current = nodes.find((node) => node.id === workflow?.current_node_id) || nodes[0];
  if (!current || current.free_discussion || current.kind === "free_discussion") return false;

  const followupIDs = [];
  if (current.next_node_id) {
    followupIDs.push(current.next_node_id);
  } else if (["continue", "summarize", "free_discussion"].includes(String(current.decision || "").trim())) {
    return true;
  }

  for (const choice of Array.isArray(current.choices) ? current.choices : []) {
    const targetID = String(choice?.target_node_id || "").trim();
    if (!targetID) return true;
    followupIDs.push(targetID);
  }

  if (!followupIDs.length) return Boolean(workflow?.preparing || workflow?.pending_node_id);
  return followupIDs.some((id) => !workflowNodeReady(nodes.find((node) => node.id === id)));
}

export function workflowNodeReady(node) {
  if (!node) return false;
  if (node.free_discussion || node.kind === "free_discussion") return true;
  return node.status === "ready" && node.voice_status === "ready";
}
