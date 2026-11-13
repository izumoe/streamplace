import { BioEditor } from "@/components/bio/bio-editor";
import { Button } from "@/components/ui/button";
import { Loader } from "@/components/ui/loader";
import { useStore } from "@/lib/store";
import { useOAuthSession, usePDSAgent } from "@/lib/store/hooks";
import { requireBioImageFile } from "@streamplace/core";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { com, place } from "streamplace";

export function BioSettings() {
  const { t } = useTranslation("settings");
  const agent = usePDSAgent();
  const recordAgent = useStore((state) => state.anonPDSAgent);
  const session = useOAuthSession();
  const did = session?.did;
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: ["bioEditor", did],
    enabled: !!did && !!agent,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
    staleTime: Infinity,
    gcTime: 0,
    retry: false,
    queryFn: async () => {
      if (!agent || !did) throw new Error("Please log in to edit your bio.");
      try {
        const result = await agent.client.call(com.atproto.repo.getRecord, {
          repo: did,
          collection: "place.stream.bio.page",
          rkey: "self",
        });
        if (!result.cid)
          throw new Error(
            "The bio record is missing its revision. Please reload and try again.",
          );
        place.stream.bio.page.$parse(result.value);
        return {
          bio: result.value as place.stream.bio.page.Main,
          cid: result.cid,
        };
      } catch (error) {
        if (
          error &&
          typeof error === "object" &&
          "error" in error &&
          error.error === "RecordNotFound"
        ) {
          return {
            bio: place.stream.bio.page.$build({
              createdAt:
                new Date().toISOString() as place.stream.bio.page.Main["createdAt"],
            }),
            cid: null,
          };
        }
        throw error;
      }
    },
  });

  if (!agent || !did)
    return (
      <p className="text-muted-foreground text-sm">
        {t("bio-login-required", "Please log in to edit your bio.")}
      </p>
    );
  if (query.isPending) return <Loader label={t("loading", "Loading")} />;
  if (query.isError)
    return (
      <div className="space-y-3">
        <p role="alert" className="text-destructive text-sm">
          {query.error.message}
        </p>
        <Button onClick={() => void query.refetch()}>
          {t("bio-retry", "Try again")}
        </Button>
      </div>
    );

  return (
    <div className="space-y-6">
      <div className="mx-auto w-full max-w-2xl px-4">
        <h1 className="font-display text-xl font-semibold">
          {t("common:profile")}
        </h1>
        <p className="text-muted-foreground mt-2 text-sm">
          {t(
            "bio-editor-description",
            "Write your bio and edit your profile cards in place.",
          )}
        </p>
      </div>
      <BioEditor
        key={did}
        did={did}
        initialBio={query.data.bio}
        onSave={async (record) => {
          try {
            const result = await agent.client.call(com.atproto.repo.putRecord, {
              repo: did,
              collection: "place.stream.bio.page",
              rkey: "self",
              record,
              swapRecord: query.data.cid,
            });
            queryClient.setQueryData(["bioEditor", did], {
              bio: record,
              cid: result.cid,
            });
            void queryClient.invalidateQueries({ queryKey: ["bio"] });
          } catch (error) {
            if (
              error &&
              typeof error === "object" &&
              "error" in error &&
              error.error === "InvalidSwap"
            )
              throw new Error(
                t(
                  "bio-save-conflict",
                  "Your bio changed in another editor. Keep a copy of your edits, then reload to get the latest version.",
                ),
              );
            throw error;
          }
        }}
        onResolveRecord={async (source) => {
          if (!recordAgent)
            throw new Error(
              t(
                "bio-record-not-ready",
                "Record lookup is not ready. Please try again.",
              ),
            );
          const result = await recordAgent.client.call(
            com.atproto.repo.getRecord,
            source,
          );
          if (!result.cid)
            throw new Error(
              t(
                "bio-record-missing-revision",
                "This record could not be added. Please try again.",
              ),
            );
          return { uri: result.uri, cid: result.cid };
        }}
        onUploadImage={async (file) => {
          requireBioImageFile(
            file,
            t("bio-image-limit", "Choose an image smaller than 1 MB."),
          );
          const url = URL.createObjectURL(file);
          try {
            const dimensions = await new Promise<{
              width: number;
              height: number;
            }>((resolve, reject) => {
              const image = new Image();
              image.onload = () =>
                resolve({
                  width: image.naturalWidth,
                  height: image.naturalHeight,
                });
              image.onerror = () =>
                reject(
                  new Error(
                    t(
                      "bio-image-unreadable",
                      "This image could not be opened.",
                    ),
                  ),
                );
              image.src = url;
            });
            if (!dimensions.width || !dimensions.height)
              throw new Error(
                t("bio-image-unreadable", "This image could not be opened."),
              );
            const response = await agent.client.uploadBlob(file, {
              encoding: file.type as `${string}/${string}`,
            });
            const { blob } = response.body;
            return {
              $type: "place.stream.bio.blocks.image",
              image: blob,
              aspectRatio: dimensions,
              alt: "",
            };
          } finally {
            URL.revokeObjectURL(url);
          }
        }}
      />
    </div>
  );
}
