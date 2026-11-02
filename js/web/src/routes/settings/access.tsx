import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { useNodeAccess } from "@/hooks/use-node-access";
import {
  useOAuthSession,
  usePDSAgent,
  useStreamplaceUrl,
} from "@/lib/store/hooks";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { place } from "streamplace";

export const Route = createFileRoute("/settings/access")({
  component: NodeAccessSettings,
});
type Grant = place.stream.access.defs.Grant;

function NodeAccessSettings() {
  const { t } = useTranslation("settings");
  const agent = usePDSAgent();
  const did = useOAuthSession()?.did;
  const url = useStreamplaceUrl();
  const status = useNodeAccess();
  const queryClient = useQueryClient();
  const grants = useQuery({
    queryKey: ["node-access-grants", url, did ?? null],
    enabled: status.isAdmin,
    retry: false,
    queryFn: async () => {
      if (!agent) throw new Error("Sign in to manage node access.");
      return agent.client.call(place.stream.access.listGrants, {});
    },
  });
  const [subject, setSubject] = useState("");
  const [capability, setCapability] = useState<"streamer" | "chat">("streamer");
  const [effect, setEffect] = useState<"allow" | "deny">("allow");
  const [reason, setReason] = useState("");
  const [expires, setExpires] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const refresh = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["node-access", url] }),
      queryClient.invalidateQueries({ queryKey: ["node-access-grants", url] }),
    ]);
  };
  const create = async (event: FormEvent) => {
    event.preventDefault();
    if (!agent || busy || !status.nodeMatchesSession) return;
    setBusy(true);
    setError(null);
    try {
      await agent.client.call(place.stream.access.createGrant, {
        subject: subject.trim() as `did:${string}:${string}`,
        capability,
        effect,
        ...(reason ? { reason } : {}),
        ...(expires
          ? {
              expiresAt: new Date(expires).toISOString() as Grant["expiresAt"],
            }
          : {}),
      });
      await refresh();
    } catch (error) {
      setError(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(false);
    }
  };
  const revoke = async (entry: Grant) => {
    if (
      !agent ||
      busy ||
      !status.nodeMatchesSession ||
      entry.source !== "stored"
    )
      return;
    setBusy(true);
    setError(null);
    try {
      await agent.client.call(place.stream.access.deleteGrant, {
        id: entry.id,
      });
      await refresh();
    } catch (error) {
      setError(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(false);
    }
  };

  if (!did || !agent)
    return <p>{t("node-access-sign-in", "Sign in to manage node access.")}</p>;
  if (!status.nodeMatchesSession)
    return (
      <div className="space-y-4">
        <p role="alert">
          {t(
            "node-access-wrong-node",
            "Reload to sign in to this node before managing access.",
          )}
        </p>
        <Button onClick={() => window.location.reload()}>
          {t("node-access-reload", "Reload")}
        </Button>
      </div>
    );
  if (status.isPending)
    return (
      <p role="status">{t("node-access-loading", "Loading node access…")}</p>
    );
  if (status.error) return <p role="alert">{status.error.message}</p>;
  if (!status.isAdmin)
    return (
      <p>
        {t(
          "node-access-admin-required",
          "Node administrator access is required.",
        )}
      </p>
    );
  return (
    <div className="space-y-6">
      <h1 className="font-display text-xl font-semibold">
        {t("node-access-title", "Node access")}
      </h1>
      <p className="text-muted-foreground text-sm">
        {t(
          "node-access-description",
          "Approve accounts to publish on this node, or restrict chat. Viewing remains public.",
        )}
      </p>
      <Card className="p-4">
        <form onSubmit={create} className="space-y-4">
          <label className="block space-y-2">
            <span>{t("node-access-subject", "Account DID")}</span>
            <Input
              value={subject}
              onChange={(event) => setSubject(event.target.value)}
              placeholder="did:plc:…"
              required
              disabled={busy}
            />
          </label>
          <label className="block space-y-2">
            <span>{t("node-access-capability", "Capability")}</span>
            <select
              className="border-input bg-background h-12 w-full rounded-md border px-3"
              value={capability}
              disabled={busy}
              onChange={(event) =>
                setCapability(event.target.value as "streamer" | "chat")
              }
            >
              <option value="streamer">
                {t("node-access-publishing", "Publishing")}
              </option>
              <option value="chat">{t("node-access-chat", "Chat")}</option>
            </select>
          </label>
          <label className="block space-y-2">
            <span>{t("node-access-action", "Action")}</span>
            <select
              className="border-input bg-background h-12 w-full rounded-md border px-3"
              value={effect}
              disabled={busy}
              onChange={(event) =>
                setEffect(event.target.value as "allow" | "deny")
              }
            >
              <option value="allow">
                {capability === "chat"
                  ? t("node-access-allow-chat", "Allow chat")
                  : t("node-access-approve-publishing", "Approve publishing")}
              </option>
              <option value="deny">
                {capability === "chat"
                  ? t("node-access-ban-chat", "Ban chat")
                  : t("node-access-deny-publishing", "Deny publishing")}
              </option>
            </select>
          </label>
          <label className="block space-y-2">
            <span>{t("node-access-reason", "Reason (optional)")}</span>
            <Input
              value={reason}
              maxLength={1024}
              onChange={(event) => setReason(event.target.value)}
              disabled={busy}
            />
          </label>
          <label className="block space-y-2">
            <span>
              {t("node-access-expires", "Expires at (optional, local time)")}
            </span>
            <Input
              type="datetime-local"
              value={expires}
              onChange={(event) => setExpires(event.target.value)}
              disabled={busy}
            />
          </label>
          <Button type="submit" disabled={busy || !subject.trim()}>
            {t("node-access-save", "Save access entry")}
          </Button>
        </form>
      </Card>
      {(error || grants.error) && (
        <p role="alert" className="text-destructive">
          {error ?? grants.error?.message}
        </p>
      )}
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">
          {t("node-access-entries", "Access entries")}
        </h2>
        <Button
          variant="outline"
          disabled={busy || grants.isFetching}
          onClick={() => void refresh()}
        >
          {t("node-access-refresh", "Refresh")}
        </Button>
      </div>
      {grants.isPending && (
        <p role="status">{t("node-access-loading", "Loading node access…")}</p>
      )}
      <div className="space-y-4">
        {grants.data?.grants.map((entry) => {
          const capabilityLabel =
            entry.capability === "streamer"
              ? t("node-access-publishing", "Publishing")
              : entry.capability === "chat"
                ? t("node-access-chat", "Chat")
                : entry.capability;
          const decision = entry.effectiveAllowed
            ? t("node-access-permitted", "permitted")
            : t("node-access-denied", "denied");
          const revokeLabel =
            entry.capability === "chat" && entry.effect === "deny"
              ? t("node-access-unban-chat", "Unban chat")
              : entry.effect === "deny"
                ? t("node-access-remove-denial", "Remove denial")
                : t("node-access-revoke-approval", "Revoke approval");
          return (
            <Card
              key={
                entry.id ||
                `${entry.source}:${entry.subject}:${entry.capability}:${entry.effect}`
              }
              data-entry-id={entry.id}
              className="space-y-2 p-4"
            >
              <p className="font-mono text-sm break-all">{entry.subject}</p>
              <p>
                {capabilityLabel} {decision}
              </p>
              <p className="text-muted-foreground text-sm">
                {entry.effect === "allow"
                  ? t("node-access-allowance", "Allowance")
                  : t("node-access-denial", "Denial")}{" "}
                ·{" "}
                {entry.source === "stored"
                  ? t("node-access-stored", "Saved on this node")
                  : t("node-access-configuration", "Configuration entry")}
                {entry.reason ? ` · ${entry.reason}` : ""}
              </p>
              {entry.expiresAt && (
                <p className="text-muted-foreground text-sm">
                  {t("node-access-expiry", "Expiry")}:{" "}
                  <time dateTime={entry.expiresAt}>
                    {new Date(entry.expiresAt).toLocaleString()}
                  </time>
                </p>
              )}
              <Button
                variant="outline"
                disabled={busy || entry.source !== "stored"}
                onClick={() => void revoke(entry)}
              >
                {entry.source !== "stored"
                  ? t("node-access-configuration", "Configuration entry")
                  : revokeLabel}
              </Button>
            </Card>
          );
        })}
      </div>
    </div>
  );
}
