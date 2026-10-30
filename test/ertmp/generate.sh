#!/usr/bin/env bash
# Small moving pictures with a fixed color border for exact-track browser tests.
set -euo pipefail
cd "$(dirname "$0")/../fixtures"

for spec in red:h264 blue:h264 green:av1; do
  color="${spec%:*}"
  codec="${spec#*:}"
  encoder=(-c:v libx264 -preset ultrafast -crf 28 -bf 0 -g 10 -keyint_min 10 -sc_threshold 0)
  if [ "$codec" = av1 ]; then
    encoder=(-c:v libaom-av1 -cpu-used 8 -crf 40 -b:v 0 -g 10 -lag-in-frames 0)
  fi
  ffmpeg -hide_banner -loglevel error -y \
    -f lavfi -i "color=c=$color:s=160x120:r=10:d=2" \
    -f lavfi -i "testsrc2=s=80x80:r=10:d=2" \
    -f lavfi -i "sine=frequency=440:sample_rate=48000:duration=2" \
    -filter_complex '[0:v][1:v]overlay=40:20:shortest=1[v]' \
    -map '[v]' -map 2:a "${encoder[@]}" -pix_fmt yuv420p \
    -c:a aac -b:a 64k -movflags frag_keyframe+delay_moov+default_base_moof \
    "ertmp-$color-$codec.mp4"
done
