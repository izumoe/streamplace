import { expect, test } from "@playwright/test";
import {
  appendFile,
  readFile,
  readdir,
  readlink,
  writeFile,
} from "node:fs/promises";
import { createLatencyExperiment } from "./live-latency-browser";
import { makeLatencyMarker } from "./live-latency-marker";

test.use({ trace: "off", video: "off" });
test.skip(
  process.env.E2E_MEMORY !== "1" || process.platform !== "linux",
  "opt-in Linux media memory diagnostics",
);

// Characterize retention rather than asserting an arbitrary RSS budget. Native
// allocator caches and swapped pages are invisible to the Go heap profile.
test("media memory through subscriber and ingest teardown", async ({
  page,
  browser,
}, testInfo) => {
  expect(process.env.E2E_LATENCY).toBe("1");
  const seconds = Number(process.env.E2E_MEMORY_SECONDS ?? 120);
  expect(Number.isInteger(seconds) && seconds > 0).toBe(true);
  test.setTimeout((seconds + 240) * 1000);
  const server = process.env.SERVER_URL!;
  const peer = process.env.SERVER2_URL!;
  const did = process.env.ACCOUNT_DID!;
  const key = process.env.E2E_STREAM_KEY!;
  expect(Boolean(server && peer && did && key)).toBe(true);
  const nodes: { pid: number; role: string; admin: string }[] = [];
  for (const pid of await readdir("/proc")) {
    if (!/^\d+$/.test(pid)) continue;
    try {
      const env = new Map(
        (await readFile(`/proc/${pid}/environ`, "utf8"))
          .split("\0")
          .map((line) => {
            const at = line.indexOf("=");
            return [line.slice(0, at), line.slice(at + 1)] as const;
          }),
      );
      const url = `http://${env.get("SP_HTTP_ADDR")}`;
      if (url !== server && url !== peer) continue;
      if (
        (await readFile(`/proc/${pid}/cmdline`, "utf8"))
          .split("\0")
          .includes("ingest-worker")
      )
        continue;
      nodes.push({
        pid: Number(pid),
        role: url === server ? "main" : "peer",
        admin: `http://${env.get("SP_HTTP_INTERNAL_ADDR")}`,
      });
    } catch (error) {
      // /proc entries can disappear while enumerating unrelated processes.
      if (
        !["ENOENT", "ESRCH"].includes(
          (error as NodeJS.ErrnoException).code ?? "",
        )
      )
        throw error;
    }
  }
  expect(nodes.map((node) => node.role).sort()).toEqual(["main", "peer"]);
  const main = nodes.find((node) => node.role === "main")!;
  const peerNode = nodes.find((node) => node.role === "peer")!;
  const output = testInfo.outputPath("media-memory.jsonl");
  await writeFile(output, "");
  const delay = (ms: number) =>
    new Promise((resolve) => setTimeout(resolve, ms));
  async function sample(phase: string) {
    const processes = [];
    for (const node of nodes) {
      const status = await readFile(`/proc/${node.pid}/status`, "utf8");
      const smaps = await readFile(`/proc/${node.pid}/smaps_rollup`, "utf8");
      const metrics = await fetch(`${node.admin}/metrics`, {
        signal: AbortSignal.timeout(5000),
      });
      expect(metrics.ok).toBe(true);
      processes.push({
        ...node,
        status,
        smaps,
        metrics: (await metrics.text())
          .split("\n")
          .filter((line) =>
            /^(go_memstats_|go_goroutines |process_resident_memory_bytes )/.test(
              line,
            ),
          ),
      });
    }
    const workers: number[] = [];
    for (const pid of await readdir("/proc")) {
      if (!/^\d+$/.test(pid)) continue;
      try {
        const status = await readFile(`/proc/${pid}/status`, "utf8");
        if (Number(status.match(/^PPid:\s+(\d+)/m)?.[1]) !== main.pid) continue;
        if (
          !(await readFile(`/proc/${pid}/cmdline`, "utf8"))
            .split("\0")
            .includes("ingest-worker")
        )
          continue;
        workers.push(Number(pid));
        processes.push({
          pid: Number(pid),
          role: "worker",
          admin: "",
          status,
          smaps: await readFile(`/proc/${pid}/smaps_rollup`, "utf8"),
          metrics: [],
        });
      } catch (error) {
        if (
          !["ENOENT", "ESRCH"].includes(
            (error as NodeJS.ErrnoException).code ?? "",
          )
        )
          throw error;
      }
    }
    await appendFile(
      output,
      JSON.stringify({ at: Date.now(), phase, processes }) + "\n",
    );
    return workers;
  }
  async function phase(name: string, duration: number) {
    const until = Date.now() + duration * 1000;
    let workers: number[] = [];
    do {
      workers = await sample(name);
      await delay(5000);
    } while (Date.now() < until);
    const response = await fetch(`${main.admin}/debug/pprof/heap`);
    expect(response.ok).toBe(true);
    await writeFile(
      testInfo.outputPath(`${name}.heap.pb`),
      Buffer.from(await response.arrayBuffer()),
    );
    return workers;
  }
  await writeFile(
    testInfo.outputPath("process-maps.txt"),
    `binary: ${await readlink(`/proc/${main.pid}/exe`)}\n` +
      (await readFile(`/proc/${main.pid}/maps`, "utf8")),
  );
  const viewers = await browser.newContext();
  let publisherStarted = false;
  try {
    await phase("idle", 15);
    await page.goto(`${server}/api/healthz`);
    await page.addScriptTag({
      content: `window.latencyExperiment = (${createLatencyExperiment.toString()})((${makeLatencyMarker.toString()})(), ${JSON.stringify(server)}, ${JSON.stringify(did)}, true);`,
    });
    await page.evaluate((key) => window.latencyExperiment.start(key), key);
    publisherStarted = true;
    const viewer = await viewers.newPage();
    await viewer.goto(`${server}/embed/${encodeURIComponent(did)}/`);
    await expect(
      viewer.locator('[data-playback-transport="moq"] canvas'),
    ).toHaveCSS("opacity", "1");
    expect(
      (await phase("ingest-and-subscribers", seconds)).length,
    ).toBeGreaterThan(0);
    await viewers.close();
    // This is the harness's isolated peer, identified by its exact exported
    // address. Stop its replication subscription as well as browser playback.
    process.kill(peerNode.pid, "SIGTERM");
    nodes.splice(nodes.indexOf(peerNode), 1);
    await expect
      .poll(async () => {
        try {
          const status = await readFile(`/proc/${peerNode.pid}/status`, "utf8");
          // The harness waits for this child during its own cleanup, so an
          // exited peer can remain as a zombie until the test returns.
          return /^State:\s+Z/m.test(status);
        } catch (error) {
          if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
          return true;
        }
      })
      .toBe(true);
    await phase("ingest-without-subscribers", 60);
    await page.evaluate(() => window.latencyExperiment.stop());
    publisherStarted = false;
    await page.goto("about:blank");
    expect(await phase("ingest-stopped", 90)).toEqual([]);
  } finally {
    if (publisherStarted)
      await page.evaluate(() => window.latencyExperiment.stop());
    await viewers.close();
    await testInfo.attach("media-memory", {
      path: output,
      contentType: "application/x-ndjson",
    });
  }
});
