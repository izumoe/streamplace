#!/usr/bin/env bash
# Small moving pictures with a fixed color border for exact-track browser tests.
set -euo pipefail
mode=${1:-}
if [[ "$mode" = --abr || "$mode" = --replication || "$mode" = --replication-unmatched ]]; then
  mkdir -p "${2:?usage: generate.sh --abr|--replication|--replication-unmatched OUTPUT_DIRECTORY}"
  cd "$2"
else
  cd "$(dirname "$0")/../fixtures"
fi

for spec in red:h264 blue:h264 green:av1; do
  color="${spec%:*}"
  codec="${spec#*:}"
  if [[ "$mode" = --replication || "$mode" = --replication-unmatched ]]; then codec=h264; fi
  encoder=(-c:v libx264 -preset ultrafast -crf 28 -bf 0 -g 10 -keyint_min 10 -sc_threshold 0)
  if [ "$codec" = av1 ]; then
    encoder=(-c:v libaom-av1 -cpu-used 8 -crf 40 -b:v 0 -g 10 -lag-in-frames 0)
  fi
  size=160x120
  pattern=80x80
  rate=10
  duration=2
  audio=(-c:a aac -b:a 64k)
  overlay=40:20
  if [ "$mode" = --abr ]; then
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
  if [[ "$mode" = --replication || "$mode" = --replication-unmatched ]]; then
    rate=30
    gop=30
    duration=20
    if [[ "$mode" = --replication-unmatched && "$color" != green ]]; then
      rate=60000/1001
      gop=60
    fi
    case "$color" in
      red) size=1920x1080; pattern=1904x1064 ;;
      blue) size=1280x720; pattern=1264x704 ;;
      green) size=640x360; pattern=624x344 ;;
    esac
    overlay=8:8
    encoder=(-c:v libx264 -threads 2 -preset veryfast -profile:v high -tune zerolatency -b:v 1200k -bf 0 -g "$gop" -keyint_min "$gop" -sc_threshold 0 -video_track_timescale 90000)
    audio=(-c:a aac -b:a 128k -ac 2)
  fi
  ffmpeg -hide_banner -loglevel error -y \
    -f lavfi -i "color=c=$color:s=$size:r=$rate:d=$duration" \
    -f lavfi -i "testsrc2=s=$pattern:r=$rate:d=$duration" \
    -f lavfi -i "sine=frequency=440:sample_rate=48000:duration=$duration" \
    -filter_complex "[0:v][1:v]overlay=$overlay:shortest=1[v]" \
    -map '[v]' -map 2:a "${encoder[@]}" -pix_fmt yuv420p \
    "${audio[@]}" -movflags frag_keyframe+delay_moov+default_base_moof \
    "ertmp-$color-$codec.mp4"
done
