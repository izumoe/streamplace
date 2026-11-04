import { getBlob, type DIDDocument } from "@streamplace/core";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { com, StreamplaceAgent } from "streamplace";
import { useStore } from "../lib/store";

export function useBioImage(did: string, cid: string) {
  const nodeUrl = useStore((state) => state.url);
  const query = useQuery({
    queryKey: ["bioImage", nodeUrl, did, cid],
    gcTime: 0,
    staleTime: Infinity,
    retry: false,
    queryFn: async () => {
      const identity = await new StreamplaceAgent(nodeUrl).client.call(
        com.atproto.identity.refreshIdentity,
        {
          identifier:
            did as com.atproto.identity.refreshIdentity.$InputBody["identifier"],
        },
      );
      if (identity.didDoc.id !== did)
        throw new Error("The resolved image owner does not match its DID.");
      return getBlob(did, cid, identity.didDoc as unknown as DIDDocument);
    },
  });
  const [source, setSource] = useState<{ blob: Blob; url: string }>();
  useEffect(() => {
    if (!query.data) return;
    const url = URL.createObjectURL(query.data);
    setSource({ blob: query.data, url });
    return () => URL.revokeObjectURL(url);
  }, [query.data]);
  return {
    src: source?.blob === query.data ? source?.url : undefined,
    isError: query.isError,
  };
}
