const MOTIONS = new Set(["still", "float", "pulse", "bounce"]);
const TAU = Math.PI * 2;
const ENTRY_DURATION_MS = 360;

export const STATIC_CHARACTER_MOTION = Object.freeze({
  offsetXRatio: 0,
  offsetYRatio: 0,
  scaleX: 1,
  scaleY: 1,
});

function finiteElapsed(elapsedMs) {
  if (typeof elapsedMs !== "number" || !Number.isFinite(elapsedMs) || elapsedMs < 0) {
    throw new TypeError("character motion elapsed time must be a non-negative finite number");
  }
  return elapsedMs;
}

export function projectCharacterMotion(motion, elapsedMs, reducedMotion = false) {
  if (!MOTIONS.has(motion)) {
    throw new TypeError("character motion is unsupported");
  }
  const elapsed = finiteElapsed(elapsedMs);
  if (reducedMotion) return STATIC_CHARACTER_MOTION;

  let offsetYRatio = 0;
  let scaleX = 1;
  let scaleY = 1;

  switch (motion) {
    case "float": {
      const phase = TAU * ((elapsed % 3600) / 3600);
      offsetYRatio = -0.005 * (1 - Math.cos(phase)) / 2;
      scaleY += 0.003 * Math.sin(phase);
      scaleX -= 0.0015 * Math.sin(phase);
      break;
    }
    case "pulse": {
      const phase = TAU * ((elapsed % 2400) / 2400);
      const pulse = Math.sin(phase);
      scaleX += 0.006 * pulse;
      scaleY -= 0.004 * pulse;
      break;
    }
    case "bounce": {
      const phase = TAU * ((elapsed % 1800) / 1800);
      const lift = Math.max(0, Math.sin(phase)) ** 2;
      offsetYRatio = -0.012 * lift;
      scaleX += 0.006 * lift;
      scaleY -= 0.009 * lift;
      break;
    }
    case "still":
      break;
  }

  if (elapsed < ENTRY_DURATION_MS) {
    const remaining = 1 - elapsed / ENTRY_DURATION_MS;
    const settle = remaining * remaining;
    offsetYRatio += 0.008 * settle;
    scaleX *= 1 - 0.012 * settle;
    scaleY *= 1 + 0.012 * settle;
  }

  return Object.freeze({ offsetXRatio: 0, offsetYRatio, scaleX, scaleY });
}
