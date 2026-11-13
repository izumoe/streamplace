import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useNodeAccess } from "@/hooks/use-node-access";
import { isWebUrl } from "@/lib/branding";
import { useStore } from "@/lib/store";
import {
  useOAuthSession,
  usePDSAgent,
  useStreamplaceUrl,
} from "@/lib/store/hooks";
import { useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useId, useRef, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { place } from "streamplace";

export const Route = createFileRoute("/settings/branding")({
  component: BrandingAdmin,
});
type Asset = place.stream.branding.getBranding.BrandingAsset;
type Upload = {
  data: string;
  mimeType: string;
  width?: number;
  height?: number;
};
type Write = (key: string, upload?: Upload) => Promise<void>;

function BrandingAdmin() {
  const url = useStreamplaceUrl();
  const session = useOAuthSession();
  return (
    <BrandingEditor
      key={`${url}:${session?.did}:${session?.serverMetadata.issuer}`}
    />
  );
}

function BrandingEditor() {
  const { t } = useTranslation("settings");
  const agent = usePDSAgent();
  const url = useStreamplaceUrl();
  const session = useOAuthSession();
  const status = useNodeAccess();
  const [busy, setBusy] = useState(false);
  const writing = useRef(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const branding = useQuery({
    queryKey: ["admin-branding", url, session?.did],
    enabled: status.isAdmin,
    retry: false,
    refetchOnWindowFocus: false,
    queryFn: async () => {
      if (!agent) throw new Error("Sign in to this node.");
      return agent.client.call(place.stream.branding.getBranding, {});
    },
  });
  const write: Write = async (key, upload) => {
    if (
      !agent ||
      !status.isAdmin ||
      writing.current ||
      useStore.getState().url !== url ||
      useStore.getState().oauthSession?.did !== session?.did
    )
      return;
    writing.current = true;
    setBusy(true);
    setError(null);
    setSaved(false);
    try {
      if (upload)
        await agent.client.call(place.stream.branding.updateBlob, {
          key,
          ...upload,
        });
      else await agent.client.call(place.stream.branding.deleteBlob, { key });
      setSaved(true);
      await branding.refetch({ throwOnError: true });
      if (
        useStore.getState().url === url &&
        useStore.getState().oauthSession?.did === session?.did
      ) {
        await useStore.getState().fetchBranding({ force: true });
      }
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
      return;
    } finally {
      writing.current = false;
      setBusy(false);
    }
  };

  if (!agent || !session)
    return <p>{t("branding-sign-in", "Sign in to manage site branding.")}</p>;
  if (!status.nodeMatchesSession)
    return (
      <p role="alert">
        {t(
          "branding-wrong-node",
          "Sign in to this node before managing its branding.",
        )}
      </p>
    );
  if (status.isPending)
    return (
      <p role="status">
        {t("branding-checking", "Checking administrator access…")}
      </p>
    );
  if (!status.isAdmin)
    return (
      <div className="space-y-4">
        <p role="alert">
          {t(
            "branding-admin-required",
            "Node administrator access is required.",
          )}
        </p>
        {status.isError && (
          <Button onClick={() => status.refetch()}>
            {t("branding-retry", "Retry")}
          </Button>
        )}
      </div>
    );
  if (branding.isPending)
    return (
      <p role="status">{t("branding-loading", "Loading site branding…")}</p>
    );
  if (branding.isError)
    return (
      <div className="space-y-4">
        <p role="alert">{branding.error.message}</p>
        {saved && (
          <p>
            {t(
              "branding-saved-reload-failed",
              "Your change was saved. Retry loading the current values.",
            )}
          </p>
        )}
        <Button onClick={() => branding.refetch()}>
          {t("branding-retry", "Retry")}
        </Button>
      </div>
    );
  const assets = Object.fromEntries(
    branding.data.assets.map((asset) => [asset.key, asset]),
  );
  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-semibold">
          {t("site-branding-title", "Site branding")}
        </h1>
        <p className="text-muted-foreground mt-1 text-sm">
          {t(
            "branding-node-description",
            "Public identity and links for this node.",
          )}
        </p>
      </div>
      {error && (
        <p role="alert" className="text-destructive text-sm">
          {error}
        </p>
      )}
      {saved && !error && (
        <p role="status" className="text-muted-foreground text-sm">
          {t("branding-saved", "Saved")}
        </p>
      )}
      <fieldset disabled={busy} className="space-y-6">
        <div className="divide-border divide-y rounded-lg border">
          <BrandingTextField
            key={assets.siteTitle?.data ?? ""}
            asset={assets.siteTitle}
            assetKey="siteTitle"
            label={t("branding-site-name", "Site name")}
            write={write}
          />
          <BrandingTextField
            key={assets.siteDescription?.data ?? ""}
            asset={assets.siteDescription}
            assetKey="siteDescription"
            label={t("branding-description", "Site description")}
            write={write}
          />
        </div>
        <div className="grid gap-4 sm:grid-cols-3">
          {(
            [
              ["mainLogo", t("branding-logo", "Logo"), 500 * 1024],
              ["favicon", t("branding-icon", "Favicon"), 100 * 1024],
              [
                "linkBanner",
                t("branding-share-image", "Share image"),
                2 * 1024 * 1024,
              ],
            ] as const
          ).map(([key, label, maxSize]) => (
            <BrandingImageField
              key={key}
              assetKey={key}
              label={label}
              asset={assets[key]}
              maxSize={maxSize}
              url={url}
              version={branding.dataUpdatedAt}
              write={write}
            />
          ))}
        </div>
        <BrandingLinks
          key={`social:${assets.socialLinks?.data}`}
          asset={assets.socialLinks}
          kind="social"
          write={write}
        />
        <BrandingLinks
          key={`legal:${assets.legalLinks?.data}`}
          asset={assets.legalLinks}
          kind="legal"
          write={write}
        />
      </fieldset>
    </div>
  );
}

function textUpload(value: string): Upload {
  const bytes = new TextEncoder().encode(value);
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return { data: btoa(binary), mimeType: "text/plain" };
}

function BrandingTextField({
  asset,
  assetKey,
  label,
  write,
}: {
  asset?: Asset;
  assetKey: string;
  label: string;
  write: Write;
}) {
  const { t } = useTranslation("settings");
  const id = useId();
  const [value, setValue] = useState(asset?.data ?? "");
  const save = (event: FormEvent) => {
    event.preventDefault();
    void write(assetKey, textUpload(value.trim()));
  };
  return (
    <form onSubmit={save} className="space-y-2 p-4">
      <label htmlFor={id} className="text-sm font-medium">
        {label}
      </label>
      <div className="flex flex-wrap gap-2">
        <Input
          id={id}
          aria-label={label}
          value={value}
          maxLength={1024}
          onChange={(event) => setValue(event.target.value)}
          className="min-w-0 flex-1"
        />
        <Button
          type="submit"
          size="sm"
          aria-label={
            assetKey === "siteTitle"
              ? t("branding-save-name", "Save site name")
              : t("branding-save-description", "Save site description")
          }
          disabled={value.trim() === (asset?.data ?? "")}
        >
          {t("branding-save", "Save")}
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={!asset}
          onClick={() => void write(assetKey)}
        >
          {t("branding-reset", "Reset")}
        </Button>
      </div>
    </form>
  );
}

function BrandingImageField({
  asset,
  assetKey,
  label,
  maxSize,
  url,
  version,
  write,
}: {
  asset?: Asset;
  assetKey: string;
  label: string;
  maxSize: number;
  url: string;
  version: number;
  write: Write;
}) {
  const { t } = useTranslation("settings");
  const id = useId();
  const [error, setError] = useState<string | null>(null);
  const [reading, setReading] = useState(false);
  const upload = async (file: File) => {
    setError(null);
    if (!file.size || file.size > maxSize) {
      setError(
        t(
          "branding-image-too-large",
          "Choose a non-empty image within the size limit.",
        ),
      );
      return;
    }
    if (
      ![
        "image/png",
        "image/jpeg",
        "image/webp",
        ...(assetKey === "favicon"
          ? ["image/x-icon", "image/vnd.microsoft.icon"]
          : []),
      ].includes(file.type)
    ) {
      setError(
        t(
          "branding-image-format",
          "Choose a PNG, JPEG or WebP image (ICO is also supported for the favicon).",
        ),
      );
      return;
    }
    setReading(true);
    try {
      const dataURL = await new Promise<string>((resolve, reject) => {
        const reader = new FileReader();
        reader.onload = () => resolve(String(reader.result));
        reader.onerror = () =>
          reject(
            new Error(
              t("branding-image-read-failed", "Could not read the image."),
            ),
          );
        reader.readAsDataURL(file);
      });
      const image = new Image();
      image.src = dataURL;
      await image.decode();
      await write(assetKey, {
        data: dataURL.slice(dataURL.indexOf(",") + 1),
        mimeType: file.type,
        width: image.naturalWidth,
        height: image.naturalHeight,
      });
    } catch {
      setError(t("branding-image-read-failed", "Could not read the image."));
    } finally {
      setReading(false);
    }
  };
  const preview = asset?.url ? new URL(asset.url, url) : null;
  if (preview) preview.searchParams.set("v", String(version));
  return (
    <section className="space-y-3 rounded-lg border p-4">
      <h2 className="text-sm font-medium">{label}</h2>
      {preview && (
        <img
          src={preview.href}
          alt={label}
          className="bg-muted h-24 w-full rounded-md object-contain"
        />
      )}
      <p className="text-muted-foreground text-xs">
        {t("branding-max-size", {
          defaultValue: "Maximum {{size}} KB",
          size: maxSize / 1024,
        })}
      </p>
      {assetKey === "linkBanner" && (
        <p className="text-muted-foreground text-xs">
          {t("branding-share-size", "Recommended: 1200 × 630.")}
        </p>
      )}
      <label htmlFor={id} className="text-sm">
        {t("branding-replace-image", "Choose image")}
      </label>
      <Input
        id={id}
        aria-label={label}
        type="file"
        disabled={reading}
        accept={
          assetKey === "favicon"
            ? "image/png,image/jpeg,image/webp,image/x-icon,image/vnd.microsoft.icon"
            : "image/png,image/jpeg,image/webp"
        }
        onChange={(event) => {
          const file = event.target.files?.[0];
          event.target.value = "";
          if (file) void upload(file);
        }}
      />
      {error && (
        <p role="alert" className="text-destructive text-sm">
          {error}
        </p>
      )}
      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled={!asset || reading}
        aria-label={
          assetKey === "mainLogo"
            ? t("branding-reset-logo", "Reset logo")
            : assetKey === "favicon"
              ? t("branding-reset-icon", "Reset favicon")
              : t("branding-reset-share", "Reset share image")
        }
        onClick={() => void write(assetKey)}
      >
        {t("branding-reset", "Reset")}
      </Button>
    </section>
  );
}

type LinkDraft = { label: string; url: string; icon?: string };
function BrandingLinks({
  asset,
  kind,
  write,
}: {
  asset?: Asset;
  kind: "social" | "legal";
  write: Write;
}) {
  const { t } = useTranslation("settings");
  const [links, setLinks] = useState<LinkDraft[] | null>(() => {
    try {
      const stored: unknown = JSON.parse(asset?.data ?? "[]");
      if (
        !Array.isArray(stored) ||
        stored.some(
          (link) =>
            !link ||
            typeof link.url !== "string" ||
            typeof (kind === "legal" ? link.text : link.label) !== "string",
        )
      )
        return null;
      return stored.map((link) => ({
        label: kind === "legal" ? link.text : link.label,
        url: link.url,
        ...(typeof link.icon === "string" ? { icon: link.icon } : {}),
      }));
    } catch {
      return null;
    }
  });
  const [error, setError] = useState<string | null>(null);
  const title =
    kind === "social"
      ? t("branding-social-links", "Social links")
      : t("branding-legal-links", "Legal links");
  if (!links)
    return (
      <section className="space-y-4 rounded-lg border p-4">
        <h2 className="text-sm font-medium">{title}</h2>
        <p role="alert">
          {t(
            "branding-invalid-saved-links",
            "Could not read the saved links. Reset them to start again.",
          )}
        </p>
        <Button
          type="button"
          variant="outline"
          onClick={() => void write(`${kind}Links`)}
        >
          {t("branding-reset", "Reset")}
        </Button>
      </section>
    );
  const linkName =
    kind === "social"
      ? t("branding-social-link", "Social link")
      : t("branding-legal-link", "Legal link");
  const update = (index: number, patch: Partial<LinkDraft>) =>
    setLinks(
      links.map((link, i) => (i === index ? { ...link, ...patch } : link)),
    );
  const save = (event: FormEvent) => {
    event.preventDefault();
    setError(null);
    if (links.some((link) => !link.label.trim() || !isWebUrl(link.url))) {
      setError(
        t(
          "branding-invalid-link",
          "Use an http or https URL and a label for every link.",
        ),
      );
      return;
    }
    const value = JSON.stringify(
      links.map((link) =>
        kind === "legal"
          ? { text: link.label.trim(), url: link.url.trim() }
          : { ...link, label: link.label.trim(), url: link.url.trim() },
      ),
    );
    if (new TextEncoder().encode(value).length > 16 * 1024) {
      setError(t("branding-links-too-large", "Links must fit within 16 KB."));
      return;
    }
    void write(`${kind}Links`, textUpload(value));
  };
  return (
    <form onSubmit={save} className="space-y-4 rounded-lg border p-4">
      <h2 className="text-sm font-medium">{title}</h2>
      {links.map((link, index) => (
        <div key={index} className="flex flex-wrap gap-2">
          <Input
            value={link.label}
            className="min-w-0 flex-1"
            aria-label={t("branding-link-label-number", {
              defaultValue: "{{kind}} {{number}} label",
              kind: linkName,
              number: index + 1,
            })}
            placeholder={t("branding-link-label", "Label")}
            onChange={(event) => update(index, { label: event.target.value })}
          />
          <Input
            value={link.url}
            className="min-w-0 flex-1"
            aria-label={t("branding-link-url-number", {
              defaultValue: "{{kind}} {{number}} URL",
              kind: linkName,
              number: index + 1,
            })}
            placeholder="https://"
            onChange={(event) => update(index, { url: event.target.value })}
          />
          <Button
            type="button"
            variant="outline"
            size="sm"
            aria-label={t("branding-remove-link-number", {
              defaultValue: "Remove {{kind}} {{number}}",
              kind: linkName,
              number: index + 1,
            })}
            onClick={() => setLinks(links.filter((_, i) => i !== index))}
          >
            {t("branding-remove", "Remove")}
          </Button>
        </div>
      ))}
      {error && (
        <p role="alert" className="text-destructive text-sm">
          {error}
        </p>
      )}
      <div className="flex flex-wrap gap-2">
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => setLinks([...links, { label: "", url: "" }])}
        >
          {kind === "social"
            ? t("branding-add-social", "Add social link")
            : t("branding-add-legal", "Add legal link")}
        </Button>
        <Button type="submit" size="sm">
          {kind === "social"
            ? t("branding-save-social", "Save social links")
            : t("branding-save-legal", "Save legal links")}
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={!asset}
          onClick={() => void write(`${kind}Links`)}
        >
          {t("branding-reset", "Reset")}
        </Button>
      </div>
    </form>
  );
}
