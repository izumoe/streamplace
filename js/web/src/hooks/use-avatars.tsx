// Cache and batch-fetch profile data for a list of DIDs.
import { AppBskyActorDefs } from "@atproto/api";
import { useEffect } from "react";
import { useStore } from "../lib/store";
import { useCachedProfiles } from "../lib/store/hooks";

export default function useAvatars(
  dids: string[],
): Record<string, AppBskyActorDefs.ProfileViewDetailed> {
  const getProfiles = useStore((state) => state.getProfiles);
  const profiles = useCachedProfiles();

  // Cache updates and new caller arrays must not refetch the same omitted DIDs.
  const missingDids = JSON.stringify(dids.filter((did) => !(did in profiles)));

  useEffect(() => {
    const actors: string[] = JSON.parse(missingDids);
    if (actors.length > 0) {
      void getProfiles(actors);
    }
  }, [missingDids, getProfiles]);

  return profiles;
}
