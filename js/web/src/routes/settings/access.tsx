import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useNodeAccess } from "@/hooks/use-node-access";
import { useNodeActorSearch } from "@/hooks/use-node-actor-search";
import {
  useOAuthSession,
  usePDSAgent,
  useStreamplaceUrl,
} from "@/lib/store/hooks";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useRef, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { place } from "streamplace";

export const Route = createFileRoute("/settings/access")({
  component: NodeAccessSettings,
});
type Grant = place.stream.access.defs.Grant;
const editableCapabilities = ["streamer", "chat", "syndicate", "vod"] as const;
type Capability = (typeof editableCapabilities)[number];
type Mode = "open" | "allowlist" | "off";

function NodeAccessSettings() {
  const url = useStreamplaceUrl();
  const session = useOAuthSession();
  return (
    <NodeAccessEditor
      key={`${url}:${session?.did ?? ""}:${session?.serverMetadata.issuer ?? ""}`}
    />
  );
}

function NodeAccessEditor() {
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
  const editor = useRef<HTMLDetailsElement>(null);
  const [subject, setSubject] = useState("");
  const [accountQuery, setAccountQuery] = useState("");
  const actors = useNodeActorSearch(
    status.isAdmin && !subject && !accountQuery.trim().startsWith("did:")
      ? accountQuery
      : "",
    8,
  );
  const [draftModes, setDraftModes] = useState<
    Partial<Record<Capability, { mode: Mode; revision: number }>>
  >({});
  const [capability, setCapability] = useState<Capability>("streamer");
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
  const saveMode = async (capability: Capability, mode?: Mode) => {
    if (!agent || busy || !status.isAdmin || !status.data) return;
    setBusy(true);
    setError(null);
    try {
      await agent.client.call(place.stream.access.setMode, {
        capability,
        ...(mode ? { mode } : {}),
        expectedRevision: String(
          mode
            ? (draftModes[capability]?.revision ?? status.data.revision)
            : status.data.revision,
        ),
      });
    } catch (error) {
      setError(error instanceof Error ? error.message : String(error));
    } finally {
      setDraftModes((current) => {
        const next = { ...current };
        delete next[capability];
        return next;
      });
      await refresh();
      setBusy(false);
    }
  };
  const create = async (event: FormEvent) => {
    event.preventDefault();
    if (!agent || busy || !status.isAdmin) return;
    setBusy(true);
    setError(null);
    try {
      await agent.client.call(place.stream.access.createGrant, {
        subject: (subject || accountQuery.trim()) as `did:${string}:${string}`,
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
    if (!agent || busy || !status.isAdmin || entry.source !== "stored") return;
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

  const capabilityLabels: Record<string, string> = {
    streamer: t("node-access-publishing", "Publishing"),
    chat: t("node-access-chat", "Chat"),
    syndicate: t("node-access-carriage", "Remote carriage"),
    vod: t("node-access-vod", "Uploads and recordings"),
    admin: t("node-access-administration", "Node administration"),
    viewer: t("node-access-public-viewing", "Public viewing"),
  };
  const columns: string[] = [...editableCapabilities, "admin"];
  const people = new Map<string, Map<string, Grant[]>>();
  for (const entry of grants.data?.grants ?? []) {
    if (!columns.includes(entry.capability)) columns.push(entry.capability);
    let permissions = people.get(entry.subject);
    if (!permissions) {
      permissions = new Map();
      people.set(entry.subject, permissions);
    }
    const entries = permissions.get(entry.capability) ?? [];
    entries.push(entry);
    permissions.set(entry.capability, entries);
  }
  const startRule = (subject: string, capability: Capability) => {
    if (busy || !status.isAdmin) return;
    setSubject(subject);
    setAccountQuery(subject);
    setCapability(capability);
    setEffect("allow");
    setReason("");
    setExpires("");
    setError(null);
    if (editor.current) {
      editor.current.open = true;
      editor.current.querySelector("input")?.focus();
    }
  };
  const streamingMode = status.data?.capabilities.find(
    (entry) => entry.capability === "streamer",
  )?.mode;

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
      <div className="space-y-4">
        <p>
          {t(
            "node-access-admin-required",
            "Node administrator access is required.",
          )}
        </p>
        <p className="text-muted-foreground text-sm">
          {t(
            "node-access-admin-setup",
            "Ask the operator to add your DID to SP_ADMIN_DIDS and restart the node. Signing in alone does not grant administrator access.",
          )}
        </p>
        <code className="block break-all">{did}</code>
      </div>
    );
  return (
    <div className="min-w-0 space-y-6">
      <h1 className="font-display text-xl font-semibold">
        {t("node-access-title", "Node access")}
      </h1>
      <p className="text-muted-foreground text-sm">
        {t(
          "node-access-description",
          "Approve accounts to publish on this node, or restrict chat. Viewing remains public.",
        )}
      </p>
      {(error || grants.error) && (
        <p role="alert" className="text-destructive">
          {error ?? grants.error?.message}
        </p>
      )}
      <details
        data-access-policy-editor
        className="max-w-3xl space-y-4 rounded-md border p-4"
      >
        <summary className="cursor-pointer font-medium">
          <span>{t("node-access-policy", "Streaming policy")}</span>
          {" · "}
          <span className="text-muted-foreground">
            {streamingMode === "open"
              ? t("node-access-any-account", "Any authenticated account")
              : streamingMode === "off"
                ? t("node-access-disabled", "Disabled")
                : t("node-access-approved-only", "Approved accounts only")}
          </span>
        </summary>
        <p className="text-muted-foreground text-sm">
          {t(
            "node-access-policy-help",
            "Saved policies take effect without a restart and override startup defaults. Restrictions disconnect existing publishers; reopening requires them to reconnect.",
          )}
        </p>
        {editableCapabilities.map((cap) => {
          const policy = status.data?.capabilities.find(
            (entry) => entry.capability === cap,
          );
          if (!policy) return null;
          const label =
            cap === "streamer"
              ? t("node-access-who-streams", "Who can stream")
              : cap === "chat"
                ? t("node-access-who-chats", "Who can chat")
                : cap === "syndicate"
                  ? t(
                      "node-access-remote-streams",
                      "Remote streams carried here",
                    )
                  : t(
                      "node-access-uploads",
                      "Who can start uploads and recordings",
                    );
          const value = draftModes[cap]?.mode ?? policy.mode;
          const modeLabels = {
            allowlist: t("node-access-approved-only", "Approved accounts only"),
            open:
              cap === "streamer" || cap === "chat"
                ? t("node-access-any-account", "Any authenticated account")
                : t("node-access-any-source", "Any account"),
            off: t("node-access-disabled", "Disabled"),
          };
          const control = (
            <fieldset key={cap} disabled={busy} className="space-y-2">
              <label className="block space-y-2">
                <span className="font-medium">{label}</span>
                <select
                  aria-label={label}
                  value={value}
                  onChange={(event) =>
                    setDraftModes((current) => ({
                      ...current,
                      [cap]: {
                        mode: event.target.value as Mode,
                        revision: status.data!.revision,
                      },
                    }))
                  }
                  className="border-input bg-background h-12 w-full rounded-md border px-3"
                >
                  {Object.entries(modeLabels).map(([mode, label]) => (
                    <option key={mode} value={mode}>
                      {label}
                    </option>
                  ))}
                </select>
              </label>
              <p className="text-muted-foreground text-sm">
                {policy.modeSource === "stored"
                  ? t("node-access-stored", "Saved on this node")
                  : t("node-access-startup-default", "Using startup default")}
              </p>
              <div className="flex flex-wrap gap-2">
                <Button
                  data-policy-save={cap}
                  disabled={busy || value === policy.mode}
                  onClick={() => void saveMode(cap, value as Mode)}
                >
                  {t("node-access-save-policy", "Save policy")}
                </Button>
                {policy.modeSource === "stored" && (
                  <Button
                    variant="outline"
                    data-policy-reset={cap}
                    onClick={() => void saveMode(cap)}
                  >
                    {t("node-access-reset-policy", "Use startup default")}:{" "}
                    {modeLabels[policy.defaultMode as Mode] ??
                      policy.defaultMode}
                  </Button>
                )}
              </div>
            </fieldset>
          );
          return cap === "streamer" ? (
            control
          ) : (
            <details key={cap}>
              <summary className="cursor-pointer font-medium">{label}</summary>
              <div className="pt-4">{control}</div>
            </details>
          );
        })}
        <p className="text-muted-foreground text-sm">
          {t(
            "node-access-policy-limits",
            "Explicit denials still apply in open mode. Public viewing stays open. Network services disabled at startup remain disabled.",
          )}
        </p>
      </details>
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">
          {t("node-access-people", "People and approvals")}
        </h2>
        <Button
          variant="outline"
          disabled={busy || grants.isFetching}
          onClick={() => void refresh()}
        >
          {t("node-access-refresh", "Refresh")}
        </Button>
      </div>
      <details
        ref={editor}
        data-access-editor
        className="rounded-md border p-4"
      >
        <summary className="cursor-pointer font-medium">
          {t("node-access-add-account", "Add account")}
        </summary>
        <form onSubmit={create} className="max-w-3xl space-y-4 pt-4">
          <label className="block space-y-2">
            <span>{t("node-access-find-account", "Find an account")}</span>
            <Input
              aria-label={t("node-access-find-account", "Find an account")}
              value={accountQuery}
              onChange={(event) => {
                setAccountQuery(event.target.value);
                setSubject("");
              }}
              placeholder={t(
                "node-access-account-placeholder",
                "Handle or DID",
              )}
              required
              disabled={busy}
            />
          </label>
          {!subject &&
            accountQuery &&
            !accountQuery.trim().startsWith("did:") && (
              <div className="space-y-2" aria-live="polite">
                {actors.searching && (
                  <p role="status">
                    {t("node-access-searching", "Searching accounts…")}
                  </p>
                )}
                {actors.error && (
                  <p role="alert">
                    {t(
                      "node-access-search-error",
                      "Search is unavailable. You can paste an account DID instead.",
                    )}
                  </p>
                )}
                {!actors.searching &&
                  !actors.error &&
                  actors.results.length === 0 && (
                    <p>
                      {t(
                        "node-access-no-accounts",
                        "No matching accounts. You can paste a DID instead.",
                      )}
                    </p>
                  )}
                {actors.results.map((actor) => (
                  <Button
                    type="button"
                    variant="outline"
                    key={actor.did}
                    onClick={() => {
                      setSubject(actor.did);
                      setAccountQuery(actor.handle || actor.did);
                    }}
                  >
                    {actor.handle || actor.did}
                  </Button>
                ))}
              </div>
            )}
          {subject && (
            <p className="text-muted-foreground font-mono text-sm break-all">
              {subject}
            </p>
          )}
          <label className="block space-y-2">
            <span>{t("node-access-capability", "Capability")}</span>
            <select
              className="border-input bg-background h-12 w-full rounded-md border px-3"
              name="capability"
              value={capability}
              disabled={busy}
              onChange={(event) =>
                setCapability(event.target.value as Capability)
              }
            >
              {editableCapabilities.map((cap) => (
                <option key={cap} value={cap}>
                  {capabilityLabels[cap]}
                </option>
              ))}
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
                  : capability === "streamer"
                    ? t("node-access-approve-publishing", "Approve publishing")
                    : t("node-access-approve", "Approve account")}
              </option>
              <option value="deny">
                {capability === "chat"
                  ? t("node-access-ban-chat", "Ban chat")
                  : capability === "streamer"
                    ? t("node-access-deny-publishing", "Deny publishing")
                    : t("node-access-deny", "Deny account")}
              </option>
            </select>
          </label>
          <details className="space-y-4">
            <summary className="cursor-pointer text-sm">
              {t("node-access-entry-options", "Reason and expiry (optional)")}
            </summary>
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
          </details>
          <Button
            type="submit"
            disabled={
              busy || !(subject || accountQuery.trim().startsWith("did:"))
            }
          >
            {t("node-access-save", "Save access entry")}
          </Button>
        </form>
      </details>
      {grants.isPending && (
        <p role="status">{t("node-access-loading", "Loading node access…")}</p>
      )}
      <div className="max-h-screen max-w-full overflow-auto rounded-md border">
        <table className="w-full border-separate border-spacing-0 text-left text-sm">
          <caption className="sr-only">
            {t("node-access-entries", "Access entries")}
          </caption>
          <thead>
            <tr>
              <th
                scope="col"
                className="bg-muted sticky top-0 z-20 border-r border-b px-3 py-3 font-medium md:left-0"
              >
                {t("account", "Account")}
              </th>
              {columns.map((capability) => (
                <th
                  key={capability}
                  scope="col"
                  className="bg-muted sticky top-0 z-10 min-w-36 border-r border-b px-3 py-3 font-medium last:border-r-0"
                >
                  {capabilityLabels[capability] ?? capability}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {[...people].map(([subject, permissions]) => (
              <tr key={subject} data-access-subject={subject}>
                <th
                  scope="row"
                  className="bg-background w-56 min-w-48 border-r border-b px-3 py-3 align-top font-mono font-normal break-all md:sticky md:left-0 md:z-10"
                >
                  {subject}
                </th>
                {columns.map((capability) => {
                  const entries = permissions.get(capability);
                  const label = capabilityLabels[capability] ?? capability;
                  const editable = editableCapabilities.includes(
                    capability as Capability,
                  );
                  const mode = status.data?.capabilities.find(
                    (entry) => entry.capability === capability,
                  )?.mode;
                  const policyLabel =
                    mode === "open"
                      ? t("node-access-any-source", "Any account")
                      : mode === "off"
                        ? t("node-access-disabled", "Disabled")
                        : t(
                            "node-access-approved-only",
                            "Approved accounts only",
                          );
                  const decision = entries?.[0].effectiveAllowed
                    ? t("node-access-permitted", "permitted")
                    : t("node-access-denied", "denied");
                  return (
                    <td
                      key={capability}
                      data-access-capability={capability}
                      className="border-r border-b p-3 align-top last:border-r-0"
                    >
                      {entries ? (
                        <details>
                          <summary
                            className="min-h-11 cursor-pointer content-center font-medium md:min-h-0"
                            aria-label={`${label}: ${decision} (${entries.length})`}
                          >
                            {decision}
                            <span className="text-muted-foreground ml-2 text-xs">
                              ({entries.length})
                            </span>
                          </summary>
                          <div className="mt-3 min-w-48 space-y-3">
                            {entries.map((entry) => {
                              const revokeLabel =
                                entry.capability === "chat" &&
                                entry.effect === "deny"
                                  ? t("node-access-unban-chat", "Unban chat")
                                  : entry.effect === "deny"
                                    ? t(
                                        "node-access-remove-denial",
                                        "Remove denial",
                                      )
                                    : t(
                                        "node-access-revoke-approval",
                                        "Revoke approval",
                                      );
                              return (
                                <div
                                  key={
                                    entry.id ||
                                    `${entry.source}:${entry.capability}:${entry.effect}`
                                  }
                                  data-entry-id={entry.id}
                                  className="space-y-2 border-t pt-3"
                                >
                                  <p>
                                    {label} {decision}
                                  </p>
                                  <p className="text-muted-foreground text-xs">
                                    {entry.effect === "allow"
                                      ? t("node-access-allowance", "Allowance")
                                      : t("node-access-denial", "Denial")}{" "}
                                    ·{" "}
                                    {entry.source === "stored"
                                      ? t(
                                          "node-access-stored",
                                          "Saved on this node",
                                        )
                                      : t(
                                          "node-access-configuration",
                                          "Configuration entry",
                                        )}
                                  </p>
                                  {entry.reason && (
                                    <p className="max-w-64 break-words">
                                      {entry.reason}
                                    </p>
                                  )}
                                  {entry.expiresAt && (
                                    <p className="text-muted-foreground text-xs">
                                      {t("node-access-expiry", "Expiry")}:{" "}
                                      <time dateTime={entry.expiresAt}>
                                        {new Date(
                                          entry.expiresAt,
                                        ).toLocaleString()}
                                      </time>
                                    </p>
                                  )}
                                  {entry.source === "stored" ? (
                                    <Button
                                      variant="outline"
                                      size="sm"
                                      disabled={busy}
                                      onClick={() => void revoke(entry)}
                                    >
                                      {revokeLabel}
                                    </Button>
                                  ) : (
                                    <span className="text-muted-foreground text-xs">
                                      {t("node-access-read-only", "Read-only")}
                                    </span>
                                  )}
                                </div>
                              );
                            })}
                            {editable && (
                              <Button
                                variant="outline"
                                size="sm"
                                disabled={busy}
                                onClick={() =>
                                  startRule(subject, capability as Capability)
                                }
                              >
                                {t("node-access-set-rule", "Set rule")}
                              </Button>
                            )}
                          </div>
                        </details>
                      ) : (
                        <div className="space-y-1">
                          {editable ? (
                            <Button
                              variant="ghost"
                              size="sm"
                              className="text-muted-foreground h-auto min-h-11 justify-start p-0 md:min-h-0"
                              disabled={busy}
                              aria-label={`${t("node-access-set-rule", "Set rule")}: ${subject} · ${label}`}
                              onClick={() =>
                                startRule(subject, capability as Capability)
                              }
                            >
                              {t(
                                "node-access-default-policy",
                                "Default policy",
                              )}
                            </Button>
                          ) : (
                            <span className="text-muted-foreground">
                              {t(
                                "node-access-default-policy",
                                "Default policy",
                              )}
                            </span>
                          )}
                          <p className="text-muted-foreground text-xs">
                            {policyLabel}
                          </p>
                        </div>
                      )}
                    </td>
                  );
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
