// Observe the real speaker route and native worklet cursors.
export function observeAudioOutput() {
  window.moqAudioAnalysers = [];
  window.moqAudioBuffers = [];
  const NativeWorklet = window.AudioWorkletNode;
  window.AudioWorkletNode = new Proxy(NativeWorklet, {
    construct(target, args, newTarget) {
      const node = Reflect.construct(
        target,
        args,
        newTarget,
      ) as AudioWorkletNode;
      const buffer: NonNullable<Window["moqAudioBuffers"]>[number] = {
        context: node.context,
        stalled: true,
        positions: [],
      };
      window.moqAudioBuffers!.push(buffer);
      node.port.addEventListener("message", (event: MessageEvent) => {
        if (event.data?.type !== "state") return;
        buffer.stalled = event.data.stalled || !event.data.anchored;
        if (event.data.anchored) {
          buffer.positions.push({
            at: window.parent.performance.now(),
            timestamp: event.data.timestamp,
            stalled: event.data.stalled,
          });
        }
      });
      const post = node.port.postMessage;
      node.port.postMessage = function (...messages) {
        const message = messages[0] as {
          type?: string;
          timestamp: number;
          data: Float32Array[];
        };
        if (message?.type === "data") {
          buffer.end =
            message.timestamp +
            (message.data[0].length / node.context.sampleRate) * 1_000_000;
        }
        return Reflect.apply(post, this, messages);
      };
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
    }[];
  }
}
