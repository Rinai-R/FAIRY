export function createSpeechPlaybackState() {
  return Object.freeze({
    turnId: null,
    dataUrl: "",
    error: null,
    played: false,
  });
}

export function reduceSpeechPlayback(state, event) {
  const payload = event?.payload;
  if (!payload || event.state !== "completed") return state;
  switch (payload.type) {
    case "speech.synthesized":
      return Object.freeze({
        turnId: event.turnId,
        dataUrl: payload.dataUrl,
        error: null,
        played: false,
      });
    case "speech.failed":
      return Object.freeze({
        turnId: event.turnId,
        dataUrl: "",
        error: payload.error,
        played: false,
      });
    default:
      return state;
  }
}

export function playSpeechDataUrl(dataUrl, AudioCtor = globalThis.Audio) {
  if (typeof dataUrl !== "string" || !dataUrl.startsWith("data:audio/")) {
    return Promise.reject(new TypeError("speech audio must be a data audio URL"));
  }
  if (typeof AudioCtor !== "function") {
    return Promise.reject(new Error("audio playback is unavailable"));
  }
  const audio = new AudioCtor(dataUrl);
  const played = audio.play();
  return played && typeof played.then === "function" ? played : Promise.resolve();
}
