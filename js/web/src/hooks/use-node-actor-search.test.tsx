import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { useNodeActorSearch } from "./use-node-actor-search";

const { call, agent } = vi.hoisted(() => {
  const call = vi.fn();
  return { call, agent: { client: { call } } };
});
vi.mock("../lib/store", () => ({
  useStore: (select: (state: unknown) => unknown) =>
    select({ pdsAgent: null, anonPDSAgent: agent }),
}));
vi.mock("streamplace", () => ({
  place: { stream: { live: { searchActorsTypeahead: {} } } },
}));

let value: ReturnType<typeof useNodeActorSearch>;
let container: HTMLDivElement;
let root: ReturnType<typeof createRoot>;
function Search({ query }: { query: string }) {
  value = useNodeActorSearch(query, 8);
  return null;
}
async function render(query: string) {
  await act(async () => root.render(<Search query={query} />));
  await act(async () => vi.advanceTimersByTimeAsync(300));
}
function deferred() {
  let resolve!: (value: { actors: { did: string; handle: string }[] }) => void;
  const promise = new Promise<{ actors: { did: string; handle: string }[] }>(
    (done) => (resolve = done),
  );
  return { promise, resolve };
}
const actor = { did: "did:plc:alice", handle: "alice.example" };

beforeEach(() => {
  vi.useFakeTimers();
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  call.mockReset();
  container = document.createElement("div");
  root = createRoot(container);
});
afterEach(async () => {
  await act(async () => root.unmount());
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

it("searches the anonymous node agent with a trimmed handle prefix", async () => {
  call.mockResolvedValue({ actors: [actor] });
  await render("  @ALICE  ");
  expect(call.mock.calls[0][1]).toEqual({ q: "ALICE", limit: 8 });
  expect(value).toEqual({ results: [actor], searching: false, error: false });
});

it("keeps the latest query when an earlier response arrives late", async () => {
  const first = deferred();
  call.mockReturnValueOnce(first.promise).mockResolvedValueOnce({ actors: [] });
  await render("alice");
  expect(value.searching).toBe(true);
  await render("nobody");
  await act(async () => first.resolve({ actors: [actor] }));
  expect(value).toEqual({ results: [], searching: false, error: false });
});

it("clearing input discards pending and late results", async () => {
  const first = deferred();
  call.mockReturnValueOnce(first.promise);
  await render("alice");
  await render("");
  await act(async () => first.resolve({ actors: [actor] }));
  expect(value).toEqual({ results: [], searching: false, error: false });
});

it("distinguishes a failed search from no results and recovers on edit", async () => {
  call.mockRejectedValueOnce(new TypeError("network unavailable"));
  await render("alice");
  expect(value).toEqual({ results: [], searching: false, error: true });
  call.mockResolvedValueOnce({ actors: [actor] });
  await render("alic");
  expect(value).toEqual({ results: [actor], searching: false, error: false });
});

it("does not issue a debounced request after unmount", async () => {
  await act(async () => root.render(<Search query="alice" />));
  await act(async () => root.unmount());
  root = createRoot(container);
  await act(async () => vi.advanceTimersByTimeAsync(300));
  expect(call).not.toHaveBeenCalled();
});
