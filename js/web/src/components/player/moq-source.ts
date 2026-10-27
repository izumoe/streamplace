export const STALL_TIMEOUT_MS = 10_000;

export function getMoqBatchDuration(catalog: Record<string, unknown>) {
  const timing = catalog.streamplace;
  if (typeof timing !== "object" || timing === null) return;
  const duration = (timing as Record<string, unknown>).batchDurationMs;
  if (
    typeof duration !== "number" ||
    !Number.isSafeInteger(duration) ||
    duration <= 0 ||
    duration >= STALL_TIMEOUT_MS
  )
    return;
  return duration;
}

export function supportsMoqPlayback(): boolean {
  const browser = globalThis as Record<string, unknown>;
  return (
    browser.isSecureContext === true &&
    typeof browser.WebTransport === "function" &&
    typeof browser.VideoDecoder === "function" &&
    typeof browser.AudioDecoder === "function" &&
    typeof browser.AudioContext === "function"
  );
}

export async function getMoqPlaybackSource(src: string, signal: AbortSignal) {
  const source = new URL(src);
  const streamer = source.searchParams.get("streamer");
  if (!streamer) return;

  const discovery = new URL(
    "/xrpc/place.stream.playback.getPlaybackServer",
    source,
  );
  discovery.searchParams.set("stream", streamer);
  const response = await fetch(discovery, { signal });
  if (!response.ok) {
    throw new Error(`MoQ playback discovery failed: ${response.status}`);
  }
  const { moq } = await response.json();
  if (!moq) return;
  if (
    typeof moq.url !== "string" ||
    typeof moq.name !== "string" ||
    !moq.name.trim()
  ) {
    throw new Error("Invalid MoQ playback advertisement");
  }

  const advertised = new URL(moq.url);
  const url =
    advertised.protocol === "moqt:"
      ? new URL(`https:${advertised.href.slice("moqt:".length)}`)
      : advertised;
  if (url.protocol !== "https:") {
    throw new Error("MoQ playback requires HTTPS");
  }
  const certificateHash = url.searchParams.get("certhash") ?? undefined;
  if (
    certificateHash !== undefined &&
    !/^[a-f\d]{64}$/i.test(certificateHash)
  ) {
    throw new Error("Invalid MoQ certificate fingerprint");
  }
  url.searchParams.delete("certhash");
  return { url, name: moq.name as string, certificateHash };
}
