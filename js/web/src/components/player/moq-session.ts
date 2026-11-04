// Reporting is best-effort. The node expires a silent lease after 30 seconds;
// these requests never gate playback or trigger transport fallback.
export function createMoqSession(url: URL, sid = "") {
  let closed = false;
  let pending = false;
  let lastTouch = -Infinity;

  function report(closing: boolean) {
    return fetch(url, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ sid, closed: closing }),
      keepalive: closing,
      signal: AbortSignal.timeout(5000),
    });
  }

  function release() {
    try {
      if (sid) void report(true).catch(() => {});
    } catch {
      // Request construction can throw before fetch returns a promise.
    }
  }

  return {
    touch() {
      const now = Date.now();
      if (closed || pending || now - lastTouch < 10_000) return;
      lastTouch = now;
      pending = true;
      return (async () => {
        try {
          const response = await report(false);
          if (response.ok) {
            const value = await response.json();
            if (typeof value.sid === "string" && value.sid) sid = value.sid;
          }
        } catch {
          // Counts can lag during an outage; media keeps playing independently.
        } finally {
          pending = false;
          if (closed) release();
        }
      })();
    },
    close() {
      if (closed) return;
      closed = true;
      if (!pending) release();
    },
  };
}
