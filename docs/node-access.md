# Node access

Node access is private operator policy, stored under the node's `ServerDID` in
its state database. It does not create AT Protocol records or change stream
records, signing keys, signed media, or the distribution protocol.

Capabilities are independent. Being an administrator does not grant publishing,
chat, upload, or recording rights.

| Capability  | Policy                                                 | Management                                                                         |
| ----------- | ------------------------------------------------------ | ---------------------------------------------------------------------------------- |
| `admin`     | Allowlisted administrator DIDs                         | `SP_ADMIN_DIDS` at startup                                                         |
| `viewer`    | Public viewing                                         | Restricted viewing is unsupported                                                  |
| `chat`      | Open by default                                        | Dashboard modes, allowances, and restrictions                                      |
| `streamer`  | Allowlist by default, including when the list is empty | Dashboard modes and approvals; `SP_ALLOWED_STREAMS` seeds configuration allowances |
| `syndicate` | Incoming remote creators, off unless configured        | Dashboard modes and approvals; `SP_SYNDICATE` seeds configuration allowances       |
| `vod`       | Upload and recording eligibility                       | Dashboard modes and approvals; startup compatibility mapping below                 |

## Configure and manage access

Set `SP_ADMIN_DIDS` to the DIDs that may manage this node, then sign in through
its normal OAuth flow and open **Settings → Node access**. Approving publishing
allows a fresh local ingest connection. A publishing denial overrides a stored
or configured allowance. Removing an approval does not deny a fresh connection when another allowance or
open mode permits it. Removing any stored publishing allowance still ends its
existing admissions, which must reconnect.

Allowances and denials are separate entries. **Unban chat** removes the exact
denial; it preserves an independent chat allowance. Configuration entries are
read-only in the interface. Identical creates are idempotent. To change an
entry's reason or expiry, remove that exact entry and create another; the API
rejects an unversioned overwrite. Expiry is exclusive and stored at microsecond
precision consistently across SQLite and PostgreSQL.

`SP_ACCESS_POLICY` sets startup modes as comma-separated assignments, for example:

```sh
SP_ACCESS_POLICY=streamer=allowlist,chat=open,syndicate=off,vod=off
```

Modes are `open`, `allowlist`, or `off`. `admin` must remain `allowlist` and
`viewer` must remain `open`. Invalid, duplicate, and unsupported assignments
prevent startup. The dashboard edits `streamer`, `chat`, `syndicate`, and `vod`
modes and account entries. Administrator access and public viewing remain fixed.

Saved modes override startup defaults and survive restarts. **Use startup default**
removes that saved override; the button shows the resolved default before you
apply it. Configuration allowances remain read-only. `SP_DISABLE_SYNDICATION`
and other disabled network services still take precedence over eligibility.

Policy saves include the revision from when the edit began. A concurrent change
is rejected rather than overwritten; the dashboard refreshes the effective
settings so the operator can review and retry. Identical retries are idempotent.
Changing the selected node or sign-in clears unsaved edits and account selections.
A stricter publishing mode disconnects existing admissions, even if publishing is
reopened before their queued work finishes. Publishers must reconnect.

An open mode still requires an authenticated or verified account where the
operation requires identity. Test `did:key` identities need explicit allowances.
Stored denials override open mode, configured allowances, and `SP_WIDE_OPEN`.
Existing signing-key verification and account bans continue to apply.

## Publishing, carriage, and VOD migration

An empty `SP_ALLOWED_STREAMS` no longer opens local publishing. Administrators
must approve themselves separately if they also want to stream. The test-stream
exception no longer admits unrelated accounts.

Incoming remote carriage follows the verified creator's `syndicate` capability,
independently of local publishing approval. `SP_SYNDICATE` seeds that allowlist;
`*` opens carriage for accounts. Review this separately when migrating a node
that previously used its local publishing list to limit remote creators.
`syndicate=off` leaves public distribution of approved local streams available.
The existing `SP_DISABLE_SYNDICATION` switch still disables both directions.
Creator-signed distribution restrictions remain enforced. Live carriage changes
apply to admission and queued publication. Existing websocket pulls end when
validation observes a denial; Iroh subscriptions are removed on their next origin
update, and denied queued data is dropped before media validation. Opening
carriage follows the next origin announcement; it does not start a history replay.

VOD eligibility preserves the existing startup policy: `SP_ALLOWED_STREAMS`
seeds both publishing and VOD allowances; an empty list leaves VOD open unless
an explicit VOD mode overrides it. `SP_WIDE_OPEN` also opens VOD unless overridden.
Later stored publishing grants do not change VOD eligibility. Recording still
honors the user's existing recording opt-out. Set `vod=off` if the node should
not admit uploads or begin recordings. Changing VOD eligibility does not stop
recordings or uploads that have already started. Ending an owned livestream still queues
its normal cleanup after publishing approval is revoked.

Publisher restriction generations survive entry removal and process restarts.
Reapproval permits a fresh connection, while work from a revoked admission stays
rejected. Detached workers without admission metadata must reconnect rather than
reattach after upgrade.

## Chat and propagation

Node chat restrictions withhold live delivery, history, pins, reply previews,
and queued notifications on this node. They do not write Bluesky blocks, labels,
or deletions, and do not change channel moderator scopes. Indexed messages stay
available for deduplication. Unbanning can restore indexed history, including
withheld messages, without replaying them as new live messages.

Updated clients refresh recent chat and pins after a chat policy change. Refresh
and reconnect clear the selected reply target while preserving typed text and
playback. Older clients stop receiving new restricted chat but may retain
already cached content.

A successful local mutation installs its snapshot before acknowledgement.
Processes sharing the same authority and database refresh every 30 seconds;
expiry is checked against current time during admission and delivery. Failed
refreshes retain the last valid snapshot and log an error, so the shared-process
propagation bound depends on database availability.

The `place.stream.access` XRPC methods expose own status and administrator-only
listing, creation, and exact deletion. They use existing verified authentication.
The interface checks the signed-in session's node authority before making access
requests, requiring a new sign-in when the selected node differs.

## Acceptance checks

In the configured build container, use a current node build and run:

```sh
E2E_UNAPPROVED_STREAM=1 hack/e2e-web-local.sh \
  --config ../web/playwright.config.ts \
  --project=chromium node-access-media.spec.ts
```

Set `E2E_BUILDDIR` when the build directory differs from the harness default.
This isolated fixture starts its administrator without publishing approval and
makes the peer receive-only. The test exercises real OAuth and UI approval,
WHIP revocation and fresh admission, anonymous peer media playback, and
unchanged PDS key records. It writes sanitized acceptance evidence without
retaining traces containing the private test stream key.

`TestAccessMigrationPreservesExistingNodeState` checks an additive SQLite upgrade
with existing configuration and OAuth-session rows. These checks qualify the
current build's origin/peer behavior; mixed upstream/fork distribution still
requires a separately qualified binary pair, including its muxl versions.
