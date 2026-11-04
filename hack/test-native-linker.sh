#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd -P)
target=${1:?usage: test-native-linker.sh TARGET LINKER [RUNNER...]}
linker=${2:?usage: test-native-linker.sh TARGET LINKER [RUNNER...]}
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
source_dir="$scratch/source"
mkdir -p "$source_dir/src" "$source_dir/subprojects/moq/src" "$source_dir/rust/streamplace-native/src"

# Exercise Cargo's cc-rs build scripts through the same generated compiler
# wrapper as the native build, including a cache launcher and a spaced path.
compiler_argv=("$linker")
if compiler_cache=$(command -v sccache); then
  compiler_argv=("$compiler_cache" "${compiler_argv[@]}")
  export RUSTC_WRAPPER="$compiler_cache"
fi
quoted_command=()
for arg in "${compiler_argv[@]}"; do
  quoted_command+=("'${arg//\'/\'\"\'\"\'}'")
done
compiler_wrapper="$scratch/compiler with space"
linker_wrapper="$scratch/linker with space"
wrapper_template=$(cat "$root/rust/streamplace-native/compiler-wrapper.sh.in")
printf '%s\n' "${wrapper_template//@COMMAND@/${quoted_command[*]}}" > "$linker_wrapper"
quoted_command+=('-DFIXTURE_CC_FLAG=1')
printf '%s\n' "${wrapper_template//@COMMAND@/${quoted_command[*]}}" > "$compiler_wrapper"
chmod +x "$compiler_wrapper" "$linker_wrapper"
export CC="$compiler_wrapper" CXX="$compiler_wrapper"

# Exercise the real Cargo graphs and standalone composition without building
# media dependencies. Each library exports a distinct C function.
for crate in iroh-streamplace moq-ffi; do
  dir="$source_dir"
  value=7
  if [[ "$crate" == moq-ffi ]]; then
    dir="$source_dir/subprojects/moq"
    value=11
  fi
  cat > "$dir/Cargo.toml" <<EOF
[package]
name = "$crate"
version = "0.0.0"
edition = "2021"
[lib]
crate-type = ["rlib", "cdylib"]
[build-dependencies]
cc = "=1.9.0"
EOF
  function_name=${crate//-/_}
  extension=c
  cpp=false
  linkage=
  if [[ "$crate" == moq-ffi ]]; then
    extension=cc
    cpp=true
    linkage='extern "C"'
  fi
  cat > "$dir/build.rs" <<EOF
fn main() {
    cc::Build::new().cpp($cpp).file("value.$extension").compile("fixture_$function_name");
}
EOF
  cat > "$dir/value.$extension" <<EOF
#if FIXTURE_CC_FLAG != 1
#error missing compiler option from Meson wrapper
#endif
$linkage int value_$function_name(void) { return $value; }
EOF
  cat > "$dir/src/lib.rs" <<EOF
extern "C" { fn value_$function_name() -> i32; }
#[no_mangle]
pub extern "C" fn fixture_$function_name() -> i32 { unsafe { value_$function_name() } }
EOF
  cargo generate-lockfile --offline --manifest-path "$dir/Cargo.toml"
done
touch "$source_dir/subprojects/moq/meson.build"
cat > "$source_dir/rust/streamplace-native/src/lib.rs" <<'EOF'
pub use iroh_streamplace::*;
pub use moq_ffi::*;
EOF

linker_key="CARGO_TARGET_$(printf '%s' "$target" | tr '[:lower:]-' '[:upper:]_')_LINKER"
export "$linker_key=$linker_wrapper"
bash "$root/hack/build-native.sh" "$source_dir" "$scratch/output" debug "$target" cdylib

cat > "$scratch/check.c" <<'EOF'
int fixture_iroh_streamplace(void);
int fixture_moq_ffi(void);
int main(void) {
    return fixture_iroh_streamplace() != 7 || fixture_moq_ffi() != 11;
}
EOF
"$linker" "$scratch/check.c" -L"$scratch/output" -lstreamplace_native -o "$scratch/check"
if (( $# > 2 )); then
  shift 2
  LD_LIBRARY_PATH="$scratch/output" "$@" "$scratch/check"
else
  LD_LIBRARY_PATH="$scratch/output" "$scratch/check"
fi

if [[ "$target" == "$(rustc -vV | sed -n 's/^host: //p')" ]]; then
  # Native composition must also work without a target linker override.
  unset "$linker_key"
  rm "$scratch/output/libstreamplace_native.so"
  bash "$root/hack/build-native.sh" "$source_dir" "$scratch/output" debug "$target" cdylib
  LD_LIBRARY_PATH="$scratch/output" "$scratch/check"
fi
