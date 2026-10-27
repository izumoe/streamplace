import { getStreamplaceUrl } from "@/lib/streamplace-url";
import { useCallback, useEffect, useState } from "react";

type ResolvedActor = {
  did: string;
  handle?: string;
};

export type ActorLookupResult =
  { status: "found"; actor: ResolvedActor } | { status: "not-found" };

type FetchActor = (
  input: RequestInfo | URL,
  init?: RequestInit,
) => Promise<Response>;

export async function resolveActor(
  actor: string,
  fetchActor: FetchActor = fetch,
  signal?: AbortSignal,
): Promise<ActorLookupResult> {
  // A known identity can stream without an indexed Bluesky profile.
  if (actor.startsWith("did:")) {
    return { status: "found", actor: { did: actor } };
  }

  const url = new URL(
    "/xrpc/com.atproto.identity.resolveHandle",
    getStreamplaceUrl(),
  );
  url.searchParams.set("handle", actor);
  const response = await fetchActor(url, { signal });

  if (response.ok) {
    const identity = (await response.json()) as ResolvedActor;
    return {
      status: "found",
      actor: { did: identity.did, handle: actor },
    };
  }

  const body = (await response.json().catch(() => null)) as {
    message?: string;
  } | null;
  if (
    (response.status === 400 || response.status === 404) &&
    body?.message === "HandleNotFound"
  ) {
    return { status: "not-found" };
  }

  throw new Error(`Identity lookup failed (${response.status})`);
}

type ActorLookupState =
  { status: "loading" } | ActorLookupResult | { status: "error" };

export function useActorLookup(actor: string) {
  const [attempt, setAttempt] = useState(0);
  const [state, setState] = useState<ActorLookupState>({ status: "loading" });

  useEffect(() => {
    const controller = new AbortController();
    setState({ status: "loading" });
    resolveActor(actor, fetch, controller.signal)
      .then((result) => setState(result))
      .catch((error) => {
        if (error instanceof DOMException && error.name === "AbortError") {
          return;
        }
        setState({ status: "error" });
      });
    return () => controller.abort();
  }, [actor, attempt]);

  const retry = useCallback(() => setAttempt((value) => value + 1), []);

  return { ...state, retry };
}
