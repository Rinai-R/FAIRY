export function choiceDisplayLabel(choice, index = 0) {
  const label = cleanChoiceText(choice?.label);
  if (label) return label;
  const hint = cleanChoiceText(choice?.hint);
  if (hint) return hint;
  return `选项 ${choiceOrdinal(index)}`;
}

export function choiceDisplayHint(choice) {
  return cleanChoiceText(choice?.hint);
}

export function choicePlayerText(choice, index = 0) {
  return choiceDisplayLabel(choice, index);
}

export function confirmedChoicePlayerText(choice, index = 0, advanced = false) {
  if (!advanced) return "";
  return choicePlayerText(choice, index);
}

function cleanChoiceText(value) {
  return String(value || "").replace(/\s+/g, " ").trim();
}

function choiceOrdinal(index) {
  const value = Number(index);
  if (!Number.isFinite(value) || value < 0 || value > 25) return "?";
  return String.fromCharCode(65 + value);
}
