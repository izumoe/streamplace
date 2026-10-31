#!/usr/bin/env bash
set -euo pipefail

output="${1:?usage: generate.sh OUTPUT_DIRECTORY}"
mkdir -p "$output"
for codec in h264 av1; do
  for height in 360 720 1080 2160; do
    width=$((height * 16 / 9))
    encoder=(-c:v libx264 -preset ultrafast -crf 24 -bf 2 -g 15 -keyint_min 15 -sc_threshold 0)
    if [ "$codec" = av1 ]; then
      encoder=(-c:v libaom-av1 -cpu-used 8 -row-mt 1 -crf 40 -b:v 0 -g 15 -lag-in-frames 0)
    fi
    ffmpeg -hide_banner -loglevel error -nostdin -n \
      -f lavfi -i "testsrc2=size=${width}x${height}:rate=15" -t 1 \
      -vf "drawbox=x=0:y=0:w=iw:h=ih:color=red:t=20" \
      "${encoder[@]}" -threads 2 -pix_fmt yuv420p -an \
      -movflags +delay_moov+frag_keyframe+default_base_moof \
      "$output/$codec-$height.mp4"
  done
done
