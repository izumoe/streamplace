# @streamplace/app

Expo (SDK 55) app for iOS/Android. Native projects (`ios/`, `android/`) are
gitignored and generated with `make mobile-prebuild`; shared UI lives in
`@streamplace/components` and logic in `@streamplace/core`. The browser app
lives in `../web` and runs with `pnpm --filter @streamplace/web dev`.

## User search

Search is available without signing in. Wide windows (981px and up, matching
the sidebar breakpoint) have a persistent header Search field. Typing searches
immediately, throttled to one request per 300ms with the latest query sent at
the end of the interval. Results appear in a dropdown over the current page;
only selecting an account navigates to its stream. Escape, an outside press,
or clearing the query dismisses results. An empty field leaves page controls
usable and clearing preserves keyboard focus. Wide headers reserve the title
area for Search.

Up/Down highlights results (wrapping at either end) and keeps the highlighted
account in view; Enter opens it. Editing or dismissing clears the highlight.
Short lists fit their content on first opening and reopening; long lists scroll
within half the window height. The popup has no entrance layout animation,
which would pin it to the loading state's height on web.
Completed results stay visible and selectable while a replacement query is
pending, including during the throttle interval. The loading placeholder only
appears before a completed result exists. New responses replace the list and
clear its keyboard highlight; clearing or dismissing still removes it immediately.

On smaller windows the header field is hidden entirely. The native Search tab
still opens `/search`, where Search or the keyboard's
search/Enter key submits the query. On mobile, submitting dismisses the keyboard
to keep results and empty states visible.

Both surfaces share `components/user-search.tsx` and use
`place.stream.live.searchActorsTypeahead` to search the current node's indexed
users in its database. Matching ignores case, surrounding whitespace, and a
leading `@`. Up to 20 active accounts are shown, ordered by handle; search does
not include display names, videos, or the wider Bluesky network.

Both surfaces use `components/search-results.tsx`, the existing profile cache
and `useAvatars` hook, and the shared `Avatar` component. Profile pictures appear
when available, with initials otherwise. The existing live-users poller supplies
live status and the Avatar's red ring; search adds no separate polling.

The Maestro user-search flow checks native routes, live avatars, and the
absence of the header field on phones.

## Daily loop (iOS simulator)

Three commands, first time only for the middle one:

```
make dev            # build the backend and native media libraries
pnpm app start:fast # metro on :38081, no cache clear
pnpm app dev:ios    # boot sim (if needed) + launch dev client into metro; no native build
```

JS edits hot-reload in seconds, including edits inside `js/components` and
`js/core` (metro watches the whole monorepo and resolves `@streamplace/dev` to
source, so shared packages don't need rebuilding).

`start:fast` skips the `-c` cache clear that `start` performs. Use plain
`pnpm app start` when metro's cache is stale and you want a fresh transform.

## One-time setup

- `make dev-setup` builds the backend and native dependencies.
- `pnpm app ios` builds and installs the dev client on the simulator
  (xcodebuild, slow). Only needed once, and again whenever native code changes.

## When you need a native rebuild

Only when native code changes:

- adding/upgrading an npm package with native code (`expo-*`, `react-native-*`)
- editing `app.config.ts` or a config plugin
- editing `ios/` or `android/` directly

Then run `pnpm app ios` (or `pnpm app android`). Bump `runtimeVersion` in
`package.json` only when native dependencies change (it gates expo-updates).

Packages with native code, and React and React Native themselves, are pinned
to exact versions so that dependency updates keep OTA bundles compatible with
the shipped binaries. Move them only together with a `runtimeVersion` bump.
`npx @expo/fingerprint fingerprint:generate --platform ios` (and `android`) in
`js/app` should report the same native sources before and after an update.

## Native bundles and project generation

`make app` builds the native OTA bundles and the browser frontend. The native
export includes iOS and Android bundles, their assets, and `expoConfig.json` for
the update manifest. It does not generate native projects.

`make mobile-prebuild` generates the iOS and Android projects when native
configuration changes. The `make ios`, `make android`, and CI targets run it
before their native builds. The `android-e2e` target uses its own test-only
native configuration.

Secondary screens retain module-scope `lazyScreen` imports and screen-local
Suspense and error boundaries. Native installs include these modules in the
bundle. `.maestro/logged-out/lazy-navigation.yaml` covers first-use navigation
and deep links on iOS/Android.

## i18n

FTL strings live in `../i18n/locales` and are compiled before use. Edits won't
show up unless `pnpm -F @streamplace/i18n compile:watch` is running (or you run
`pnpm -F @streamplace/i18n compile` once).

## Visual feedback for the agent

To see what the app actually looks like after a change, drive it and capture
screenshots + video with maestro (already installed on this machine):

```
pnpm app ui:shoot home    # shell sweep: Home -> Videos -> Settings; create controls stay hidden
pnpm app ui:shoot stream  # opens the first live stream, captures the player
```

If maestro isn't installed, the script prints the official install command and
offers to run it (non-interactive runs just exit with the hint).

Each run writes into a deterministic dir:

- `artifacts/<flow>/NN-step.png` — one screenshot per step
- `artifacts/<flow>/run.mp4` — video of the whole run
- `artifacts/<flow>/hierarchy.json` — end-state accessibility tree (readable
  text, no vision needed)

Flows live in `maestro/*.yaml` and use real UI selectors (tab labels, stream
card titles). Add flows by copying an existing one; run `maestro studio` to
record taps interactively. `artifacts/` is gitignored. The app must be running
(`pnpm app dev:ios` handles launching it, and the script re-runs it).
