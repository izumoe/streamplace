// Observe the real speaker route and native worklet cursors.
export function observeAudioOutput() {
  window.moqAudioAnalysers = [];
  window.moqAudioBuffers = [];
  const pairs = new WeakMap<MessagePort, MessagePort>();
  const NativeChannel = window.MessageChannel;
  window.MessageChannel = new Proxy(NativeChannel, {
    construct(target, args, newTarget) {
      const channel = Reflect.construct(
        target,
        args,
        newTarget,
      ) as MessageChannel;
      pairs.set(channel.port1, channel.port2);
      pairs.set(channel.port2, channel.port1);
      return channel;
    },
  });
  const observed = new WeakMap<MessagePort, () => void>();
  function observePort(port: MessagePort, context: BaseAudioContext) {
    const existing = observed.get(port);
    if (existing) return existing;
    let buffer: NonNullable<Window["moqAudioBuffers"]>[number] | undefined;
    let closed = false;
    const children = new Map<
      string,
      { generation: number | undefined; retire: () => void }
    >();
    const receive = (event: MessageEvent) => {
      if (!buffer || event.data?.type !== "state") return;
      buffer.stalled = event.data.stalled || !event.data.anchored;
      if (event.data.anchored) {
        buffer.positions.push({
          at: window.parent.performance.now(),
          timestamp: event.data.timestamp,
          stalled: event.data.stalled,
        });
      }
    };
    port.addEventListener("message", receive);
    const retire = () => {
      if (closed) return;
      closed = true;
      port.removeEventListener("message", receive);
      if (buffer) {
        const index = window.moqAudioBuffers!.indexOf(buffer);
        if (index !== -1) window.moqAudioBuffers!.splice(index, 1);
      }
      for (const child of children.values()) child.retire();
      children.clear();
    };
    observed.set(port, retire);
    const close = port.close;
    port.close = function () {
      retire();
      return Reflect.apply(close, this, []);
    };
    const post = port.postMessage;
    port.postMessage = function (...messages) {
      const message = messages[0] as {
        type?: string;
        id?: string;
        generation?: number;
        port?: MessagePort;
        timestamp: number;
        data: Float32Array[];
        control: SharedArrayBuffer;
        state: SharedArrayBuffer;
        rate: number;
      };
      if (!closed) {
        if (message?.type === "attach" && message.id && message.port) {
          const input = pairs.get(message.port);
          if (input) {
            children.get(message.id)?.retire();
            children.set(message.id, {
              generation: message.generation,
              retire: observePort(input, context),
            });
          }
        } else if (message?.type === "detach" && message.id) {
          const child = children.get(message.id);
          if (child && child.generation === message.generation) {
            child.retire();
            children.delete(message.id);
          }
        } else if (
          message?.type === "init-shared" ||
          message?.type === "init-post"
        ) {
          if (!buffer) {
            buffer = { context, stalled: true, positions: [] };
            window.moqAudioBuffers!.push(buffer);
          }
          buffer.shared =
            message.type === "init-shared"
              ? {
                  control: new Int32Array(message.control),
                  state: new BigInt64Array(message.state),
                  rate: message.rate,
                }
              : undefined;
        } else if (message?.type === "data" && buffer) {
          buffer.end =
            message.timestamp +
            (message.data[0].length / context.sampleRate) * 1_000_000;
        }
      }
      return Reflect.apply(post, this, messages);
    };
    return retire;
  }
  const NativeWorklet = window.AudioWorkletNode;
  window.AudioWorkletNode = new Proxy(NativeWorklet, {
    construct(target, args, newTarget) {
      const node = Reflect.construct(
        target,
        args,
        newTarget,
      ) as AudioWorkletNode;
      // Mixer input rings use their own MessageChannel, while standalone audio uses this port.
      observePort(node.port, node.context);
      return node;
    },
  });
  const connect = AudioNode.prototype.connect;
  Object.defineProperty(AudioNode.prototype, "connect", {
    value: function (this: AudioNode, ...args: unknown[]) {
      if (args[0] instanceof AudioDestinationNode) {
        const analyser = new AnalyserNode(this.context);
        window.moqAudioAnalysers!.push(analyser);
        Reflect.apply(connect, this, [analyser]);
        return Reflect.apply(connect, analyser, args);
      }
      return Reflect.apply(connect, this, args);
    },
  });
}

declare global {
  interface Window {
    moqAudioAnalysers?: AnalyserNode[];
    moqAudioBuffers?: {
      context: BaseAudioContext;
      end?: number;
      stalled: boolean;
      positions: { at: number; timestamp: number; stalled: boolean }[];
      shared?: {
        control: Int32Array;
        state: BigInt64Array;
        rate: number;
      };
    }[];
  }
}
