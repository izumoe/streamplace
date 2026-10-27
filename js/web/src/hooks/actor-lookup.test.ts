import { describe, expect, it } from "vitest";
import { resolveActor } from "./actor-lookup";

describe("resolveActor", () => {
  it.each(["did:plc:abcdefghijklmnopqrstuvwx", "did:web:streamer.example"])(
    "keeps the known DID %s usable when AppView is unavailable",
    async (did) => {
      const result = await resolveActor(did, async () => {
        throw new TypeError("AppView is unavailable");
      });

      expect(result).toEqual({ status: "found", actor: { did } });
    },
  );

  it("resolves identity through the node without requiring an AppView profile", async () => {
    const result = await resolveActor("natalie.sh", async (input) => {
      const url = new URL(input.toString());
      expect(url.origin).toBe(window.location.origin);
      expect(url.pathname).toBe("/xrpc/com.atproto.identity.resolveHandle");
      expect(url.searchParams.get("handle")).toBe("natalie.sh");
      return Response.json({ did: "did:plc:natalie" });
    });

    expect(result).toEqual({
      status: "found",
      actor: { did: "did:plc:natalie", handle: "natalie.sh" },
    });
  });

  it.each([400, 404])(
    "distinguishes a missing identity (%s) from a temporary failure",
    async (status) => {
      const result = await resolveActor(
        "missing.example",
        async () =>
          new Response(
            JSON.stringify({
              error: "InvalidRequest",
              message: "HandleNotFound",
            }),
            { status },
          ),
      );

      expect(result).toEqual({ status: "not-found" });
    },
  );

  it("throws on service failures so the UI can offer a retry", async () => {
    await expect(
      resolveActor(
        "natalie.sh",
        async () => new Response("unavailable", { status: 503 }),
      ),
    ).rejects.toThrow("Identity lookup failed (503)");
  });
});
