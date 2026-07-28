const activeTurnStates = new Set(["interpreting", "gathering", "planning", "responding"]);
const terminalTurnStates = new Set(["completed", "failed", "interrupted"]);
const terminalTurnTypes = new Set(["completed", "failed", "interrupted", "stream.closed"]);

export function projectDesktopTurnActive(current, turn) {
  if (typeof current !== "boolean") throw new TypeError("current active state must be boolean");
  if (!turn || typeof turn !== "object") return current;
  if (terminalTurnStates.has(turn.state) || terminalTurnTypes.has(turn.type)) return false;
  if (activeTurnStates.has(turn.state)) return true;
  return current;
}
