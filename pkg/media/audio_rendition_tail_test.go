package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/bus"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/muxl"
)

// Independent rendition tracks may arrive before or after the encoder's EOS
// continuation. Every named rendition, including AV1's H264 default, must still
// play the normal Opus packets followed by the tail exactly once.
func TestRenditionAudioTailPreservesPacketOrder(t *testing.T) {
	ctx := t.Context()
	var inputs []RenditionInput
	for _, fixture := range []string{"ertmp-red-h264.mp4", "h264-opus-frag.mp4"} {
		segment := allSignedBareSegments(t, ctx, audioSignerForTest(t), getFixture(fixture))[0]
		var flat bytes.Buffer
		require.NoError(t, muxl.RunMuxlWrap(ctx, bytes.NewReader(segment), "flat", &flat))
		inputs = append(inputs, RenditionInput{MP4: flat.Bytes()})
	}
	for _, fixture := range []string{"ertmp-red-h264.mp4", "av1-aac-frag.mp4"} {
		t.Run(fixture, func(t *testing.T) {
			ms := audioSignerForTest(t)
			sources := allSignedBareSegments(t, ctx, ms, getFixture(fixture))
			cert, key := nodeSignerForTest(t)
			encoder := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
			var completed [][]byte
			var tail []byte
			tr := encoder.newStreamTranscoder(ctx, "opus", cert, key, func(_ any, data []byte, audioOnly bool) {
				if audioOnly {
					tail = data
				} else {
					completed = append(completed, data)
				}
			})
			t.Cleanup(func() { require.NoError(t, tr.Close()) })
			for _, source := range sources {
				require.NoError(t, tr.Feed(source, nil))
			}
			require.NoError(t, tr.Close())
			require.GreaterOrEqual(t, len(completed), 2)
			require.NotEmpty(t, tail)
			var addenda [][]byte
			frames := map[string]int{}
			var fallback string
			for _, segment := range completed {
				addendum, err := encoder.mintVideoRenditions(ctx, segment, inputs, cert, key)
				require.NoError(t, err)
				addenda = append(addenda, addendum)
				events, err := unwrapMuxlEvents(ctx, addendum)
				require.NoError(t, err)
				cat := catalogFromEvents(events)
				primary, ok := primaryVideoTrack(cat, 0, "avc1")
				require.True(t, ok)
				fallback = RenditionName(primary.CodedWidth, primary.CodedHeight)
				for _, video := range cat.Video.Renditions {
					name := RenditionName(video.CodedWidth, video.CodedHeight)
					id := strconv.FormatUint(uint64(video.TrackID()), 10)
					for _, event := range events {
						frames[name] += int(event.SampleCounts[id])
					}
				}
			}
			require.Len(t, frames, 2, "two existing fixture sizes provide distinct named renditions")
			var want []audioPacket
			for _, segment := range completed {
				want = append(want, canonicalAudioPackets(t, ctx, segment, "opus")...)
			}
			want = append(want, canonicalAudioPackets(t, ctx, tail, "opus")...)
			for _, order := range []string{"video first", "tail first", "tail admitted before video"} {
				for _, private := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/private=%t", order, private), func(t *testing.T) {
						ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
						defer cancel()
						mm := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
						mm.bus = bus.NewBus()
						did := ms.Streamer()
						for _, segment := range completed {
							valid, err := ValidateMP4MediaC2PA(ctx, segment)
							require.NoError(t, err)
							mm.bufferSegment(did, valid.Meta.StartTime.Time(), true, false, segment, 0, *valid.Manifest.Label)
							events, err := unwrapMuxlEvents(ctx, segment)
							require.NoError(t, err)
							mm.rememberSourceAudio(did, *valid.Manifest.Label, events)
							mm.feedLiveWindow(ctx, did, segment, valid.Meta.StartTime.Time(), true)
						}
						subscriptions := map[string]*bus.SegChan{}
						for name := range frames {
							subscriptions[name] = mm.bus.SubscribeSegment(ctx, did, name)
						}
						if fixture == "av1-aac-frag.mp4" {
							subscriptions[WebRTCSourceRendition] = mm.bus.SubscribeSegment(ctx, did, WebRTCSourceRendition)
						}
						t.Cleanup(func() {
							for name, sub := range subscriptions {
								mm.bus.UnsubscribeSegment(context.Background(), did, name, sub)
							}
						})
						observed := map[string][]*bus.Seg{}
						drain := func() {
							for name, sub := range subscriptions {
								for len(sub.C) > 0 {
									observed[name] = append(observed[name], <-sub.C)
								}
							}
						}
						publishVideo := func(index int) {
							require.NoError(t, mm.ReceiveRenditions(ctx, did, addenda[index]))
						}
						for i := range addenda[:len(addenda)-1] {
							publishVideo(i)
						}
						drain()
						oldPublic, _ := mm.GetLiveWindow(did)
						require.NotNil(t, oldPublic)
						continuation := tail
						if private {
							continuation = privateAudioTailForTest(t, ctx, completed, tail, cert, key)
						}
						notifications := mm.NewSegment()
						var pending *NewSegmentNotification
						admitTail := func() {
							require.NoError(t, mm.ReceiveAudioAddendum(ctx, did, continuation))
							select {
							case notification := <-notifications:
								pending = notification
							case <-ctx.Done():
								t.Fatal("no audio continuation notification")
							}
						}
						publishTail := func() {
							if pending == nil {
								admitTail()
							}
							require.NoError(t, mm.PublishAudioAddendum(ctx, did, pending.Muxl, pending.Segment.ID, pending.Segment.StartTime, pending.Metadata.Published))
						}
						last := len(addenda) - 1
						if order == "tail first" {
							publishTail()
							for name, sub := range subscriptions {
								require.Empty(t, sub.C, "%s must wait for its last normal audio before playing EOS packets", name)
							}
							publishVideo(last)
							for name, sub := range subscriptions {
								require.Len(t, sub.C, 1, "%s must get one last video batch with its retained EOS audio", name)
							}
						} else {
							if order == "tail admitted before video" {
								admitTail()
							}
							publishVideo(last)
							publishTail()
							for name, sub := range subscriptions {
								require.Len(t, sub.C, 2, "%s must receive last video followed by audio-only EOS", name)
							}
						}
						drain()
						_, publicWindow := mm.GetLiveWindow(did)
						require.Equal(t, !private, publicWindow, "late rendition arrival must not make a private HLS window public")
						var wantNames []string
						for name := range frames {
							wantNames = append(wantNames, name)
						}
						require.ElementsMatch(t, wantNames, mm.LiveRenditionNames(did), "a private EOS transition must retain its named HLS video")
						window, _ := mm.GetLiveWindow(did)
						require.NotNil(t, window)
						tailEvents, err := unwrapMuxlEvents(ctx, continuation)
						require.NoError(t, err)
						for _, audio := range catalogFromEvents(tailEvents).Audio.Renditions {
							track := window.Track(strconv.FormatUint(uint64(audio.TrackID()), 10))
							require.NotNil(t, track)
							var retainedSamples uint32
							for _, segment := range track.Segments {
								retainedSamples += segment.SampleCount
							}
							require.Equal(t, uint32(len(want)), retainedSamples, "HLS must keep normal Opus plus EOS exactly once")
							if private {
								oldTrack := oldPublic.Track(strconv.FormatUint(uint64(audio.TrackID()), 10))
								require.NotNil(t, oldTrack)
								var publicSamples uint32
								for _, segment := range oldTrack.Segments {
									publicSamples += segment.SampleCount
								}
								tailSamples := canonicalAudioPackets(t, ctx, continuation, "opus")
								require.Equal(t, uint32(len(want)-len(tailSamples)), publicSamples, "an existing public writer must not receive private EOS samples")
							}
						}
						lastEvents, err := unwrapMuxlEvents(ctx, addenda[last])
						require.NoError(t, err)
						for _, video := range catalogFromEvents(lastEvents).Video.Renditions {
							id := strconv.FormatUint(uint64(video.TrackID()), 10)
							track := window.Track(id)
							require.NotNil(t, track)
							var retained []byte
							for _, segment := range track.Segments {
								retained = append(retained, window.SegmentData(id, segment.Seq)...)
							}
							require.True(t, bytes.Contains(retained, concatTrackAcrossSegments(lastEvents, id)), "HLS must retain the last signed rendition video, not just its earlier catalog")
						}
						for name := range subscriptions {
							packets := observed[name]
							var got [][]byte
							videoFrames, videoBatches := 0, 0
							for i, seg := range packets {
								require.NotNil(t, seg.PacketizedData)
								if seg.AudioOnly {
									require.Empty(t, seg.PacketizedData.Video)
								} else {
									videoBatches++
									videoFrames += len(seg.PacketizedData.Video)
								}
								if i == len(packets)-1 {
									require.Equal(t, !private, seg.Published, "late video containing private EOS must remain private")
								}
								for _, sample := range seg.PacketizedData.Audio {
									got = append(got, sample.Data)
								}
							}
							wantFrames := frames[name]
							if name == WebRTCSourceRendition {
								wantFrames = frames[fallback]
							}
							require.Equal(t, len(completed), videoBatches, "%s video cannot be replayed by an audio continuation", name)
							require.Equal(t, wantFrames, videoFrames, "%s must preserve every video frame once", name)
							require.Len(t, got, len(want), "%s must retain every encoded Opus packet once", name)
							for i := range want {
								require.Equal(t, want[i].data, got[i], "%s packet %d", name, i)
							}
						}
					})
				}
			}
		})
	}
}

func privateAudioTailForTest(t *testing.T, ctx context.Context, completed [][]byte, tail, cert, key []byte) []byte {
	t.Helper()
	var ingredients []byte
	for _, segment := range completed {
		events, err := unwrapMuxlEvents(ctx, segment)
		require.NoError(t, err)
		source, err := selectEventTracks(events, 0, "", false)
		require.NoError(t, err)
		events, err = unwrapMuxlEvents(ctx, source)
		require.NoError(t, err)
		audio, err := sourceAudioIngredient(events)
		require.NoError(t, err)
		ingredients = append(ingredients, audio.data...)
	}
	var manifest map[string]any
	require.NoError(t, json.Unmarshal(transcodeManifestFor("did:web:node.test", audioTailTitle), &manifest))
	manifest["assertions"] = append(manifest["assertions"].([]any), map[string]any{"label": audioPublicationAssertion, "data": map[string]any{"published": false}})
	definition, err := json.Marshal(manifest)
	require.NoError(t, err)
	result, err := muxl.RunMuxlSignTranscode(ctx, muxl.TranscodeInput{Output: tail, Source: ingredients, CertPEM: cert, KeyPEM: key, Manifest: definition})
	require.NoError(t, err)
	return result
}
