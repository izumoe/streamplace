import { useEffect, useState } from "react";
import { place } from "streamplace";
import { useStore } from "../lib/store";

type Actor = place.stream.live.searchActorsTypeahead.Actor;

export function useNodeActorSearch(query: string, limit: number) {
  const agent = useStore((s) => s.pdsAgent ?? s.anonPDSAgent);
  const [results, setResults] = useState<Actor[]>([]);
  const [searching, setSearching] = useState(false);
  const [error, setError] = useState(false);
  const q = query.trim().replace(/^@/, "");

  useEffect(() => {
    let current = true;
    setError(false);
    if (!agent || !q) {
      setResults([]);
      setSearching(false);
      return;
    }
    setSearching(true);
    const timeout = setTimeout(async () => {
      try {
        const response = await agent.client.call(
          place.stream.live.searchActorsTypeahead,
          { q, limit },
        );
        if (current) setResults(response.actors);
      } catch {
        if (current) {
          setResults([]);
          setError(true);
        }
      } finally {
        if (current) setSearching(false);
      }
    }, 250);
    return () => {
      current = false;
      clearTimeout(timeout);
    };
  }, [agent, q, limit]);

  return { results, searching, error };
}
