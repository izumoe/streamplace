// @vitest-environment node
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { runInNewContext } from "node:vm";
import ts from "typescript";
import { describe, expect, it } from "vitest";

const RATE = 48_000;
const QUANTUM = 128;
const micros = (frames: number) => (frames * 1_000_000) / RATE;

// Load the shipped worklet, so these regressions also verify the package patch.
const entry = createRequire(import.meta.url).resolve("@moq/watch");
const index = readFileSync(entry, "utf8");
const player = index.match(/from "\.\/(player-[^"]+\.js)"/)?.[1];
if (!player) throw new Error("MoQ player bundle not found");
const source = ts.createSourceFile(
  player,
  readFileSync(join(dirname(entry), player), "utf8"),
  ts.ScriptTarget.Latest,
);
const scripts: string[] = [];
function findWorklet(node: ts.Node) {
  if (
    ts.isStringLiteral(node) &&
    /registerProcessor\(\s*["']mixer["']/.test(node.text)
  )
    scripts.push(node.text);
  ts.forEachChild(node, findWorklet);
}
findWorklet(source);
if (scripts.length !== 1)
  throw new Error("MoQ mixer worklet not found uniquely");

type Message = Record<string, unknown>;
type MixState = {
  timestamp: number;
  rendered: number;
  stalled: boolean;
};

type Transport = "shared" | "postMessage";

type SharedWriter = {
  init: Message;
  readonly timestamp: number;
  insert(timestamp: number, samples: Float32Array[]): void;
  setLatency(samples: number, rebuffer?: boolean): void;
};

// Discover the shipped shared-ring writer by its public operations, not its bundled name.
const worklet = ts.createSourceFile(
  "audio-worklet.js",
  scripts[0],
  ts.ScriptTarget.Latest,
);
const writerNames: string[] = [];
const allocatorNames: string[] = [];
function findSharedWriter(node: ts.Node) {
  if (
    ts.isVariableDeclaration(node) &&
    node.initializer &&
    ts.isClassExpression(node.initializer)
  ) {
    const declaration = node.initializer;
    if (
      ["insert", "read", "resize"].every((name) =>
        declaration.members.some(
          (member) =>
            member.name &&
            ts.isIdentifier(member.name) &&
            member.name.text === name,
        ),
      )
    )
      writerNames.push(node.name.getText(worklet));
  }
  if (ts.isFunctionDeclaration(node) && node.name && node.body) {
    const properties = new Set<string>();
    function findAllocation(child: ts.Node) {
      if (
        ts.isReturnStatement(child) &&
        child.expression &&
        ts.isObjectLiteralExpression(child.expression)
      )
        for (const property of child.expression.properties)
          if (property.name && ts.isIdentifier(property.name))
            properties.add(property.name.text);
      ts.forEachChild(child, findAllocation);
    }
    findAllocation(node.body);
    if (
      ["samples", "control", "state", "capacity", "buffered"].every((name) =>
        properties.has(name),
      )
    )
      allocatorNames.push(node.name.text);
  }
  ts.forEachChild(node, findSharedWriter);
}
findSharedWriter(worklet);
if (writerNames.length !== 1 || allocatorNames.length !== 1)
  throw new Error("MoQ shared audio ring not found uniquely");

function renderer(transport: Transport) {
  let frames = 0;
  class Port {
    onmessage?: (event: { data: Message }) => void;
    messages: Message[] = [];
    closed = false;
    postMessage(message: Message) {
      this.messages.push({ ...message, rendered: frames + QUANTUM });
    }
    close() {
      this.closed = true;
    }
    send(data: Message) {
      this.onmessage?.({ data });
    }
  }
  type Processor = {
    port: Port;
    process(inputs: Float32Array[][], outputs: Float32Array[][]): boolean;
  };
  let Mixer: (new () => Processor) | undefined;
  const context: {
    sampleRate: number;
    currentTime: number;
    AudioWorkletProcessor: new () => { port: Port };
    registerProcessor(name: string, processor: new () => Processor): void;
    shared?: {
      Ring: new (init: Message) => SharedWriter;
      allocate(
        channels: number,
        capacity: number,
        rate: number,
        buffered: boolean,
      ): Message;
    };
  } = {
    sampleRate: RATE,
    currentTime: 0,
    AudioWorkletProcessor: class {
      port = new Port();
    },
    registerProcessor(name: string, processor: new () => Processor) {
      if (name === "mixer") Mixer = processor;
    },
  };
  runInNewContext(
    `${scripts[0]};globalThis.shared = { Ring: ${writerNames[0]}, allocate: ${allocatorNames[0]} };`,
    context,
  );
  if (!Mixer) throw new Error("MoQ mixer processor did not register");
  const mixer = new Mixer();
  const ports = new Map<string, Port>();
  const writers = new Map<string, SharedWriter>();
  return {
    attach(id: string) {
      const port = new Port();
      ports.set(id, port);
      mixer.port.send({ type: "attach", id, generation: 1, port });
      if (transport === "shared") {
        const shared = new context.shared!.Ring(
          context.shared!.allocate(2, RATE * 3, RATE, true),
        );
        shared.setLatency(57_600);
        writers.set(id, shared);
        port.send({ type: "init-shared", ...shared.init });
        return;
      }
      port.send({
        type: "init-post",
        rate: RATE,
        channels: 2,
        latency: 1200,
        buffered: true,
      });
    },
    write(id: string, start: number, left: Float32Array) {
      const data = [left, left.map((sample) => -sample)];
      const shared = writers.get(id);
      if (shared) shared.insert(micros(start), data);
      else
        ports.get(id)!.send({ type: "data", timestamp: micros(start), data });
    },
    delay(milliseconds: number) {
      const shared = writers.get("main");
      if (shared) shared.setLatency((milliseconds * RATE) / 1000, false);
      else
        ports
          .get("main")!
          .send({ type: "latency", latency: milliseconds, rebuffer: false });
    },
    position() {
      return (
        writers.get("main")?.timestamp ??
        ports.get("main")?.messages.findLast((m) => m.type === "state")
          ?.timestamp
      );
    },
    mix(levels: Record<string, number>) {
      mixer.port.send({ type: "mix", levels });
    },
    render(lead: number | null = 0, playing = true) {
      context.currentTime = frames / RATE;
      mixer.port.send({
        type: "clock",
        timestamp: lead === null ? undefined : micros(frames + lead),
        time: context.currentTime,
        playing,
      });
      const output = [new Float32Array(QUANTUM), new Float32Array(QUANTUM)];
      expect(mixer.process([], [output])).toBe(true);
      frames += QUANTUM;
      return output;
    },
    state(): MixState {
      const state = mixer.port.messages.findLast(
        (message) => message.type === "mix-state",
      );
      if (!state) throw new Error("MoQ mixer did not report a state");
      return state as MixState;
    },
    detach() {
      for (const [id, port] of ports) {
        mixer.port.send({ type: "detach", id, generation: 1 });
        expect(port.closed).toBe(true);
      }
    },
  };
}

function tone(start: number, count: number, frequency = 440) {
  return Float32Array.from(
    { length: count },
    (_, i) => Math.sin(((start + i) * frequency * 2 * Math.PI) / RATE) * 0.2,
  );
}

describe.each<Transport>(["shared", "postMessage"])(
  "installed MoQ %s audio playout",
  (transport) => {
    it("does not skip queued audio when the next one-second batch arrives early", () => {
      const player = renderer(transport);
      player.attach("main");
      player.write("main", 0, tone(0, 57_600));
      for (let quantum = 0; quantum < 337; quantum++) player.render();
      const before = player.state();
      player.write("main", 57_600, tone(57_600, RATE));
      for (let quantum = 0; quantum < 19; quantum++) player.render();
      const after = player.state();
      expect(after.stalled).toBe(false);
      expect(after.timestamp - before.timestamp).toBeCloseTo(
        micros(after.rendered - before.rendered),
        5,
      );
    });

    it("retires the last PCM sample after fractional callback durations", () => {
      const player = renderer(transport);
      player.attach("main");
      player.delay(100);
      const start = 3600 * RATE,
        total = 223_514;
      let written = 0;
      const write = (count: number) => {
        player.write("main", start + written, tone(written, count));
        written += count;
      };
      write(9600);
      for (let frame = 0; frame < total + 6 * QUANTUM; frame += QUANTUM) {
        if (written < total && written - frame < 4800)
          write(Math.min(4800, total - written));
        player.render(null);
      }
      expect(player.position()).toBeCloseTo(micros(start + total), 5);
      player.detach();
    });

    it("keeps queued audio playing when the shared clock raises its delay target", () => {
      const player = renderer(transport);
      player.attach("main");
      player.write("main", 0, tone(0, 57_600));
      player.render();
      player.delay(1201);
      const output = player.render(-48);
      expect(output[0].some((sample) => Math.abs(sample) > 0.01)).toBe(true);
      expect(player.state().stalled).toBe(false);
      player.detach();
    });

    it("joins the current clock without playing stale preroll or reporting phantom progress", () => {
      const player = renderer(transport);
      player.attach("main");
      player.delay(100);
      player.write("main", 0, new Float32Array(2400).fill(0.25));
      for (let quantum = 0; quantum < 5; quantum++)
        expect(player.render(3 * RATE)[0].every((sample) => sample === 0)).toBe(
          true,
        );
      expect(player.position()).toBe(0);
      player.write("main", 2400, new Float32Array(7200).fill(0.25));
      const preroll = player.render(3 * RATE);
      expect(
        preroll.every((channel) => channel.every((sample) => sample === 0)),
      ).toBe(true);
      expect(player.state().timestamp).toBe(0);
      expect(player.state().stalled).toBe(true);
      player.write("main", 3 * RATE, new Float32Array(4800).fill(0.75));
      expect(player.render(3 * RATE)[0][0]).toBeCloseTo(0.75, 6);
      expect(player.state().timestamp).toBeGreaterThanOrEqual(micros(3 * RATE));
      player.detach();
    });

    it("recovers clock drift gradually with one stereo stretch plan for all stems", () => {
      const main = renderer(transport);
      const stems = renderer(transport);
      main.attach("main");
      for (const id of ["main", "voice", "music"]) stems.attach(id);
      stems.mix({ voice: 1, music: 1 });
      function write(start: number, count: number) {
        const voice = tone(start, count);
        const music = tone(start, count, 931);
        const full = voice.map((sample, i) => sample + music[i]);
        main.write("main", start, full);
        stems.write("main", start, full);
        stems.write("voice", start, voice);
        stems.write("music", start, music);
      }
      write(0, 57_600);
      let written = 57_600;
      let difference = 0;
      let phaseError = 0;
      let largestStep = 0;
      let last: MixState | undefined;
      for (let frame = 0; frame < 3 * RATE; frame += QUANTUM) {
        if (written - frame < RATE) {
          write(written, RATE);
          written += RATE;
        }
        const lead = frame >= RATE ? 4800 : 0;
        const a = main.render(lead);
        const b = stems.render(lead);
        for (let i = 0; i < QUANTUM; i++) {
          difference = Math.max(difference, Math.abs(a[0][i] - b[0][i]));
          phaseError = Math.max(phaseError, Math.abs(b[0][i] + b[1][i]));
        }
        const state = main.state();
        if (last && state.rendered !== last.rendered)
          largestStep = Math.max(largestStep, state.timestamp - last.timestamp);
        last = state;
      }
      const state = main.state();
      expect(state.stalled).toBe(false);
      expect(micros(state.rendered + 4800) - state.timestamp).toBeLessThan(
        25_000,
      );
      expect(largestStep).toBeLessThan(75_000);
      expect(difference).toBeLessThan(0.000001);
      expect(phaseError).toBeLessThan(0.000001);
      expect(stems.state().timestamp).toBeCloseTo(state.timestamp, 5);

      for (let quantum = 0; quantum < 19; quantum++) {
        const output = stems.render(0, false);
        expect(
          output.every((channel) => channel.every((sample) => sample === 0)),
        ).toBe(true);
      }
      const paused = stems.state().timestamp;
      for (let quantum = 0; quantum < 19; quantum++) stems.render(0, false);
      expect(stems.state().timestamp).toBe(paused);
      stems.detach();
      expect(
        stems
          .render()
          .every((channel) => channel.every((sample) => sample === 0)),
      ).toBe(true);
      main.detach();
    });
  },
);
