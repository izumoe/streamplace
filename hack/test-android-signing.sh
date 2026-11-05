#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd -P)
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
mkdir -p "$scratch/lexicons"
export ANDROID_KEYSTORE_PASSWORD='build signing&test $value'
export ANDROID_KEYSTORE_ALIAS=release

# App bundles use JAR signatures. Exercise the release guard with real keys,
# including a trusted debug certificate that must not pass as the release key.
for alias in release debug; do
  keytool -genkeypair -keystore "$scratch/my-release-key.keystore" \
    -storepass:env ANDROID_KEYSTORE_PASSWORD -keypass:env ANDROID_KEYSTORE_PASSWORD \
    -alias "$alias" -keyalg RSA -keysize 2048 -validity 365 \
    -dname "CN=signing-test-$alias" >"$scratch/key-$alias.log" 2>&1
done
printf 'bundle payload\n' >"$scratch/payload"
jar --create --file "$scratch/unsigned.aab" -C "$scratch" payload
for alias in release debug; do
  cp "$scratch/unsigned.aab" "$scratch/$alias.aab"
  jarsigner -keystore "$scratch/my-release-key.keystore" \
    -storepass:env ANDROID_KEYSTORE_PASSWORD "$scratch/$alias.aab" "$alias" \
    >"$scratch/sign-$alias.log" 2>&1
done

verify() {
  make --no-print-directory -C "$scratch" -f "$root/Makefile" \
    VERSION=test BRANCH=test UUID=test "AAB=$scratch/$1.aab" \
    --eval='verify-aab: ; $(ANDROID_VERIFY_SIGNING) "$(AAB)" "$(ANDROID_KEYSTORE_ALIAS)"' \
    verify-aab >"$scratch/verify-$1.log" 2>&1
}

verify release
for invalid in debug unsigned; do
  if verify "$invalid"; then
    echo "$invalid bundle passed the release signing check" >&2
    exit 1
  fi
done
