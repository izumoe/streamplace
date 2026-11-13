import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { BioPanelsLayout } from "./bio-panels-layout";

afterEach(() => vi.unstubAllGlobals());

it("freezes card positions while dragging and repacks the persisted order on drop", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  let resize = () => {};
  vi.stubGlobal(
    "ResizeObserver",
    class {
      constructor(callback: () => void) {
        resize = callback;
      }
      observe() {}
      disconnect() {}
    },
  );
  const computed = window.getComputedStyle;
  vi.spyOn(window, "getComputedStyle").mockImplementation((element) => {
    const style = computed(element);
    return new Proxy(style, {
      get(target, key) {
        if (key === "columnGap") return "16px";
        if (key === "getPropertyValue")
          return (name: string) =>
            name === "--bio-panel-width"
              ? "320px"
              : style.getPropertyValue(name);
        return Reflect.get(target, key);
      },
    });
  });
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(656);
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(
    function () {
      return new DOMRect(0, 0, 320, Number(this.dataset.height ?? 0));
    },
  );
  const container = document.createElement("div");
  document.body.append(container);
  const root = createRoot(container);
  const render = async (order: string[], dragging = false) =>
    act(async () => {
      root.render(
        <BioPanelsLayout dragging={dragging}>
          {order.map((id) => (
            <div key={id} data-id={id} data-height={id === "a" ? 200 : 100}>
              {id}
            </div>
          ))}
        </BioPanelsLayout>,
      );
    });
  const position = (id: string) => {
    const element = container.querySelector(`[data-id="${id}"]`) as HTMLElement;
    return [element.style.left, element.style.top];
  };
  try {
    await render(["a", "b", "c"]);
    expect(position("a")).toEqual(["0px", "0px"]);
    expect(position("c")).toEqual(["336px", "116px"]);
    await render(["a", "b", "c"], true);
    (container.querySelector('[data-id="b"]') as HTMLElement).dataset.height =
      "300";
    await act(async () => resize());
    expect(position("c")).toEqual(["336px", "116px"]);
    await render(["c", "a", "b"]);
    expect(position("c")).toEqual(["0px", "0px"]);
    expect(position("b")).toEqual(["0px", "116px"]);
  } finally {
    await act(async () => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  }
});
