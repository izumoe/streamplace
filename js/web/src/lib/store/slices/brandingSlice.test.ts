import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { storage } from "../../storage";
import { createBrandingSlice } from "./brandingSlice";

const { importWarning } = vi.hoisted(() => ({
  importWarning: vi.spyOn(console, "warn").mockImplementation(() => {}),
}));
beforeAll(() => {
  try {
    for (const [message] of importWarning.mock.calls)
      expect(String(message)).toMatch(/ponyfill implementation/);
  } finally {
    importWarning.mockRestore();
  }
});

function makeBrandingState(agent: unknown) {
  let state: Record<string, any> = {};
  const set = (next: any) => {
    Object.assign(state, typeof next === "function" ? next(state) : next);
  };
  const slice = createBrandingSlice(set as any, () => state as any, {} as any);
  Object.assign(state, slice, {
    broadcasterDID: "did:plc:broadcaster",
    url: "https://stream.place",
    pdsAgent: agent,
    anonPDSAgent: null,
  });
  return state as typeof slice;
}

describe("fetchBranding", () => {
  afterEach(() => vi.restoreAllMocks());

  it("reports a failed request after storing the error", async () => {
    const getBranding = vi.fn().mockRejectedValue(new Error("offline"));
    const state = makeBrandingState({ client: { call: getBranding } });
    const consoleError = vi
      .spyOn(console, "error")
      .mockImplementation(() => {});
    vi.spyOn(storage, "getItem").mockResolvedValue(null);

    const succeeded = await state.fetchBranding({ force: true });

    expect(succeeded).toBe(false);
    expect(state.brandingLoading).toBe(false);
    expect(state.brandingError).toBe("offline");
    expect(consoleError).toHaveBeenCalledWith(
      "Failed to fetch branding:",
      expect.any(Error),
    );
  });
});

it("does not install the previous node's branding after a node switch", async () => {
  let complete:
    | ((value: {
        assets: { key: string; mimeType: string; data: string }[];
      }) => void)
    | undefined;
  const state = makeBrandingState({
    client: {
      call: () =>
        new Promise((resolve) => {
          complete = resolve;
        }),
    },
  });
  vi.spyOn(storage, "getItem").mockResolvedValue(null);
  const write = vi.spyOn(storage, "setItem").mockResolvedValue(undefined);
  const loading = state.fetchBranding({ force: true });
  await vi.waitFor(() => expect(complete).toBeTypeOf("function"));
  // Let the asynchronous storage lookup finish before resolving the old node.
  await Promise.resolve();
  Object.assign(state, { url: "https://other.example", branding: null });
  complete!({
    assets: [
      { key: "siteTitle", mimeType: "text/plain", data: "Previous node" },
    ],
  });
  expect(await loading).toBe(false);
  expect(state.branding).toBeNull();
  expect(write).not.toHaveBeenCalled();
  vi.restoreAllMocks();
});
