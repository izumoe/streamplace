import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  parseBioRecordSource,
  type BioRecordCollection,
  type BioRecordReference as RecordReference,
  type ResolveBioRecord,
} from "@/lib/bio-record-reference";
import { useEffect, useId, useState } from "react";
import { useTranslation } from "react-i18next";

export function BioRecordReference({
  collection,
  value,
  onResolveRecord,
  onChange,
}: {
  collection: BioRecordCollection;
  value?: RecordReference;
  onResolveRecord: ResolveBioRecord;
  onChange: (value: RecordReference) => void;
}) {
  const { t } = useTranslation("settings");
  const [source, setSource] = useState(value?.uri ?? "");
  const [resolving, setResolving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const id = useId();
  useEffect(() => {
    setSource(value?.uri ?? "");
    setError(null);
  }, [value?.uri]);
  const post = collection === "app.bsky.feed.post";

  return (
    <div className="space-y-2">
      <label htmlFor={id} className="text-sm font-medium">
        {post
          ? t("bio-post-source", "Bluesky post URL or AT URI")
          : t("bio-livestream-source", "Livestream AT URI")}
      </label>
      <div className="flex flex-wrap gap-2">
        <Input
          id={id}
          className="min-w-0 flex-1"
          value={source}
          disabled={resolving}
          aria-invalid={!!error}
          aria-describedby={error ? `${id}-error` : undefined}
          placeholder={
            post
              ? "https://bsky.app/profile/…/post/…"
              : "at://…/place.stream.livestream/…"
          }
          onChange={(event) => {
            setSource(event.target.value);
            setError(null);
          }}
        />
        <Button
          type="button"
          variant="outline"
          disabled={
            resolving ||
            !source.trim() ||
            (!!value && source.trim() === value.uri)
          }
          onClick={async () => {
            setResolving(true);
            setError(null);
            try {
              const reference = await onResolveRecord(
                parseBioRecordSource(source, collection),
              );
              onChange(reference);
              setSource(value ? reference.uri : "");
            } catch (reason) {
              setError(
                reason instanceof Error ? reason.message : String(reason),
              );
            } finally {
              setResolving(false);
            }
          }}
        >
          {resolving
            ? t("bio-finding-record", "Finding record…")
            : value
              ? t("bio-update-reference", "Update reference")
              : post
                ? t("bio-add-Bluesky-post", "Add Bluesky post")
                : t("bio-add-livestream", "Add livestream")}
        </Button>
      </div>
      {error && (
        <p id={`${id}-error`} role="alert" className="text-destructive text-sm">
          {error}
        </p>
      )}
    </div>
  );
}
