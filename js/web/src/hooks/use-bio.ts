import { useQuery } from "@tanstack/react-query";
import { place, StreamplaceAgent } from "streamplace";
import { useStore } from "../lib/store";

export function useBio(repo?: string) {
  const nodeUrl = useStore((state) => state.url);
  return useQuery({
    queryKey: ["bio", nodeUrl, repo ?? null],
    enabled: !!repo,
    queryFn: async (): Promise<place.stream.bio.page.Main | null> => {
      if (!repo) return null;
      try {
        return await new StreamplaceAgent(nodeUrl).client.call(
          place.stream.bio.getPage,
          { repo: repo as place.stream.bio.getPage.$Params["repo"] },
        );
      } catch (error) {
        if (
          error &&
          typeof error === "object" &&
          "error" in error &&
          error.error === "BioNotFound"
        )
          return null;
        throw error;
      }
    },
  });
}
