#!/usr/bin/env bash
# Reuse the aligned AVC ladder and encode distinguishable full-mix/solo tones once.
set -euo pipefail
input=${1:?usage: generate.sh AVC_FIXTURE_DIRECTORY OUTPUT_MP4}
output=${2:?usage: generate.sh AVC_FIXTURE_DIRECTORY OUTPUT_MP4}
mkdir -p "$(dirname "$output")"
env LD_LIBRARY_PATH= ffmpeg -nostdin -hide_banner -loglevel error -y \
  -i "$input/ertmp-red-h264.mp4" \
  -i "$input/ertmp-blue-h264.mp4" \
  -i "$input/ertmp-green-h264.mp4" \
  -f lavfi -i 'sine=frequency=440:sample_rate=48000:duration=20,aformat=channel_layouts=stereo' \
  -f lavfi -i 'sine=frequency=880:sample_rate=48000:duration=20,aformat=channel_layouts=stereo' \
  -filter_complex '[3:a]asplit=2[voice][v];[4:a]asplit=2[music][m];[v][m]amix=inputs=2:normalize=0[mix]' \
  -map 0:v -map 1:v -map 2:v -map '[mix]' -map '[voice]' -map '[music]' \
  -t 20 -c:v copy -c:a:0 aac -b:a:0 128k \
  -c:a:1 libopus -b:a:1 96k -c:a:2 libopus -b:a:2 96k \
  -movflags frag_keyframe+empty_moov+default_base_moof "$output"
