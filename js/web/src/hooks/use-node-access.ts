import { useQuery } from "@tanstack/react-query";
import { place } from "streamplace";
import {
  useOAuthSession,
  usePDSAgent,
  useStreamplaceUrl,
} from "../lib/store/hooks";

export function useNodeAccess() {
  const agent = usePDSAgent();
  const session = useOAuthSession();
  const did = session?.did;
  const url = useStreamplaceUrl();
  let nodeMatchesSession = false;
  try {
    const node = new URL(url);
    const issuer = new URL(session?.serverMetadata.issuer ?? "");
    if (node.protocol === "http:" && node.hostname === "localhost")
      node.hostname = "127.0.0.1";
    if (issuer.protocol === "http:" && issuer.hostname === "localhost")
      issuer.hostname = "127.0.0.1";
    nodeMatchesSession = node.origin === issuer.origin;
  } catch {
    // An invalid node URL must never reuse another node's sign-in.
  }
  const status = useQuery({
    queryKey: ["node-access", url, did ?? null],
    enabled: !!agent && !!did && nodeMatchesSession,
    retry: false,
    queryFn: async () => {
      if (!agent || !nodeMatchesSession)
        throw new Error("Sign in to manage node access.");
      return agent.client.call(place.stream.access.getStatus, {});
    },
  });
  const isAdmin =
    !!agent &&
    !!did &&
    nodeMatchesSession &&
    status.data?.capabilities.some(
      (capability) => capability.capability === "admin" && capability.allowed,
    ) === true;
  return { ...status, isAdmin, nodeMatchesSession };
}
