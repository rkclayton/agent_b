const CHARS_PER_TOKEN = 3.6;

export function liveTelemetry(session, now = Date.now()) {
  const telemetry = session?.activity?.stream;
  if (!telemetry || telemetry.done) return null;
  const ageSeconds = Math.max(0, Math.floor((now - telemetry.last_chunk_at) / 1000));
  const reasoningTokens = Math.ceil(telemetry.reasoning_chars / CHARS_PER_TOKEN);
  const rate = telemetry.has_chunk ? Number(telemetry.rate || 0) : null;
  return { ageSeconds, reasoningTokens, rate, hasChunk: telemetry.has_chunk };
}

export function recordedTelemetry(session) {
  const response = [...(session?.timeline || [])]
    .reverse()
    .find((event) => event.type === "model.response");
  if (!response) return null;
  const data = response.data || {};
  const rawRate = Number(data.timings?.predicted_per_second);
  return {
    reasoningTokens: Number(data.reasoning_tokens || 0),
    reasoningTokensEstimated: !!data.reasoning_tokens_estimated,
    rate: Number.isFinite(rawRate) && rawRate > 0 ? Math.round(rawRate) : null,
  };
}
