#!/usr/bin/env bash
# Small moving pictures with a fixed color border for exact-track browser tests.
set -euo pipefail
if [ "${1:-}" = --abr ]; then
  mkdir -p "${2:?usage: generate.sh --abr OUTPUT_DIRECTORY}"
  cd "$2"
  abr=1
else
  cd "$(dirname "$0")/../fixtures"
  abr=
fi

for spec in red:h264 blue:h264 green:av1; do
  color="${spec%:*}"
  codec="${spec#*:}"
  encoder=(-c:v libx264 -preset ultrafast -crf 28 -bf 0 -g 10 -keyint_min 10 -sc_threshold 0)
  if [ "$codec" = av1 ]; then
    encoder=(-c:v libaom-av1 -cpu-used 8 -crf 40 -b:v 0 -g 10 -lag-in-frames 0)
  fi
  size=160x120
  pattern=80x80
  rate=10
  overlay=40:20
  if [ -n "$abr" ]; then
    rate=15
    case "$color" in
      red) size=160x90; pattern=144x74; bitrate=100k ;;
      blue) size=320x180; pattern=304x164; bitrate=400k ;;
      green) size=640x360; pattern=624x344; bitrate=1000k ;;
    esac
    overlay=8:8
    encoder=(-c:v libx264 -preset ultrafast -b:v "$bitrate" -maxrate "$bitrate" -bufsize "$bitrate" -bf 0 -g 15 -keyint_min 15 -sc_threshold 0)
    if [ "$codec" = av1 ]; then
      encoder=(-c:v libaom-av1 -cpu-used 8 -b:v "$bitrate" -minrate "$bitrate" -maxrate "$bitrate" -bufsize "$bitrate" -g 15 -lag-in-frames 0)
    fi
  fi
  ffmpeg -hide_banner -loglevel error -y \
    -f lavfi -i "color=c=$color:s=$size:r=$rate:d=2" \
    -f lavfi -i "testsrc2=s=$pattern:r=$rate:d=2" \
    -f lavfi -i "sine=frequency=440:sample_rate=48000:duration=2" \
    -filter_complex "[0:v][1:v]overlay=$overlay:shortest=1[v]" \
    -map '[v]' -map 2:a "${encoder[@]}" -pix_fmt yuv420p \
    -c:a aac -b:a 64k -movflags frag_keyframe+delay_moov+default_base_moof \
    "ertmp-$color-$codec.mp4"
done
