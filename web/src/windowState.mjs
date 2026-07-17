function assertExactKeys(value, keys, label) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new TypeError(`${label} must be an object`);
  }
  const actual = Object.keys(value).sort();
  const expected = [...keys].sort();
  if (
    actual.length !== expected.length ||
    actual.some((key, index) => key !== expected[index])
  ) {
    throw new TypeError(`${label} has an invalid field set`);
  }
}

export function parseProductWindowLabel(label) {
  if (label !== "companion" && label !== "control-panel" && label !== "speech") {
    throw new TypeError("unsupported FAIRY product window label");
  }
  return label;
}

export function selectRecentTranscript(transcript, limit) {
  if (!Array.isArray(transcript)) {
    throw new TypeError("transcript must be an array");
  }
  if (limit !== 4) {
    throw new TypeError("transcript limit must be 4");
  }
  return Object.freeze(transcript.slice(-limit));
}

export function parseConfigurationChange(value) {
  if (value?.category === "character") {
    assertExactKeys(value, ["category", "revision"], "character change");
    if (!Number.isSafeInteger(value.revision) || value.revision <= 0) {
      throw new TypeError("character change revision is invalid");
    }
  } else if (value?.category === "user_profile") {
    assertExactKeys(value, ["category", "revision"], "profile change");
    if (
      value.revision !== null &&
      (!Number.isSafeInteger(value.revision) || value.revision <= 0)
    ) {
      throw new TypeError("profile change revision is invalid");
    }
  } else if (value?.category === "model") {
    assertExactKeys(value, ["category", "configured", "ready"], "model change");
    if (typeof value.configured !== "boolean" || typeof value.ready !== "boolean") {
      throw new TypeError("model change status is invalid");
    }
  } else {
    throw new TypeError("unsupported configuration change category");
  }
  return Object.freeze({ ...value });
}

export function configurationRefreshTarget(change) {
  switch (change.category) {
    case "character":
      return "character";
    case "model":
      return "model";
    case "user_profile":
      return null;
    default:
      throw new TypeError("unsupported configuration refresh category");
  }
}
