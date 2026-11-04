package media

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/ingestframe"
	"stream.place/streamplace/pkg/muxl"
)

// Codec lookahead can carry source samples into the next completion. Publishing
// that completion must not make private samples public or discard their policy.
func TestAudioCompletionPreservesContributorPolicy(t *testing.T) {
	for _, test := range []struct {
		name            string
		firstPublished  bool
		firstLicense    string
		secondLicense   string
		secondPublished bool
	}{
		{"stable public", true, "", "", true},
		{"private to public", false, "", "", false},
		{"rights change", true, "all rights reserved", "CC-BY-4.0", false},
	} {
		for _, local := range []bool{false, true} {
			name := "worker"
			if local {
				name = "local"
			}
			t.Run(test.name+"/"+name, func(t *testing.T) {
				ctx := t.Context()
				ms := audioSignerForTest(t)
				manifest := ms.PrebuiltManifest
				ms.PrebuiltManifest = nil
				ms.manifestBuilder = &stubManifester{
					preLive: audioPolicyManifest(t, manifest, test.firstPublished, test.firstLicense),
					live:    audioPolicyManifest(t, manifest, true, test.secondLicense),
				}
				source := allSignedBareSegments(t, ctx, ms, getFixture("ertmp-red-h264.mp4"))
				require.GreaterOrEqual(t, len(source), 2)
				var metadata []*SegmentMetadata
				for _, segment := range source {
					valid, err := ValidateMP4MediaC2PA(ctx, segment)
					require.NoError(t, err)
					metadata = append(metadata, valid.Meta)
				}
				require.Equal(t, test.firstPublished, metadata[0].Published)
				require.True(t, metadata[1].Published, "the second source itself is public")
				cert, key := nodeSignerForTest(t)
				mm := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
				var completed [][]byte
				tr := mm.newStreamTranscoder(ctx, "opus", cert, key, func(_ any, data []byte, tail bool) {
					if !tail {
						completed = append(completed, data)
					}
				})
				t.Cleanup(func() { require.NoError(t, tr.Close()) })
				for i, segment := range source {
					var token any
					if local {
						token = &validatedSegment{meta: metadata[i]}
					}
					require.NoError(t, tr.Feed(segment, token))
				}
				require.NoError(t, tr.Close())
				require.Len(t, completed, len(source))
				for i, output := range completed {
					require.True(t, bytes.HasPrefix(output, source[i]), "policy protection must preserve the original signed source")
				}
				valid, err := ValidateMP4MediaC2PA(ctx, completed[1])
				require.NoError(t, err)
				require.Equal(t, test.secondPublished, valid.Meta.Published, "a mixed completion must keep every contributor's restrictions")
				require.True(t, metadata[1].Published, "the source token's metadata must not be mutated")
			})
		}
	}
}

func audioPolicyManifest(t *testing.T, base []byte, published bool, license string) []byte {
	t.Helper()
	var manifest map[string]any
	require.NoError(t, json.Unmarshal(base, &manifest))
	assertions := manifest["assertions"].([]any)
	actions := []any{map[string]any{"action": "c2pa.created"}}
	if published {
		actions = append(actions, map[string]any{"action": "c2pa.published"})
	}
	assertions[0].(map[string]any)["data"].(map[string]any)["actions"] = actions
	if license != "" {
		metadata := assertions[1].(map[string]any)["data"].(map[string]any)
		metadata["@context"].(map[string]any)["xmpRights"] = "http://ns.adobe.com/xap/1.0/rights/"
		metadata["xmpRights:UsageTerms"] = license
	}
	result, err := json.Marshal(manifest)
	require.NoError(t, err)
	return result
}

func TestWorkerAudioTailPreservesMixedPolicyPrivately(t *testing.T) {
	for _, change := range []string{"rights", "warning"} {
		t.Run(change, func(t *testing.T) {
			ctx := t.Context()
			ms := audioSignerForTest(t)
			first, second := ms.PrebuiltManifest, ms.PrebuiltManifest
			if change == "rights" {
				first = audioPolicyManifest(t, first, true, "all rights reserved")
				second = audioPolicyManifest(t, second, true, "CC-BY-4.0")
			} else {
				var manifest map[string]any
				require.NoError(t, json.Unmarshal(first, &manifest))
				metadata := manifest["assertions"].([]any)[1].(map[string]any)["data"].(map[string]any)
				metadata["@context"].(map[string]any)["Iptc4xmpExt"] = "http://iptc.org/std/Iptc4xmpExt/2008-02-29/"
				metadata["Iptc4xmpExt:ContentWarning"] = []any{"flashing lights"}
				var err error
				first, err = json.Marshal(manifest)
				require.NoError(t, err)
				metadata["Iptc4xmpExt:ContentWarning"] = []any{"loud sounds"}
				second, err = json.Marshal(manifest)
				require.NoError(t, err)
			}
			ms.PrebuiltManifest = nil
			ms.manifestBuilder = &stubManifester{preLive: first, live: second}
			source := allSignedBareSegments(t, ctx, ms, getFixture("ertmp-red-h264.mp4"))
			require.GreaterOrEqual(t, len(source), 2)
			want := uninterruptedAudioPackets(t, ctx, source, "opus")
			cert, key := nodeSignerForTest(t)
			worker := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
			var wire bytes.Buffer
			onSegment, flush := worker.workerSegmentSink(ctx, IngestWorkerConfig{NodeCertPEM: cert, NodeKeyPEM: key}, ingestframe.NewWriter(&wire))
			t.Cleanup(flush)
			for _, segment := range source {
				require.NoError(t, onSegment(ctx, segment))
			}
			flush()
			main, _ := publisherAccessForTest(t, ms.Streamer())
			admitted, err := main.admitPublisher(ctx, ms.Streamer())
			require.NoError(t, err)
			generation, err := main.checkPublisherContext(admitted, ms.Streamer())
			require.NoError(t, err)
			notifications := main.NewSegment()
			reader := ingestframe.NewReader(bytes.NewReader(wire.Bytes()))
			var got []audioPacket
			var tail []byte
			for {
				typ, data, err := reader.ReadFrame()
				if err == io.EOF {
					break
				}
				require.NoError(t, err)
				switch typ {
				case ingestframe.Segment:
					valid, err := ValidateMP4MediaC2PA(ctx, data)
					require.NoError(t, err)
					main.bufferSegment(ms.Streamer(), valid.Meta.StartTime.Time(), valid.Meta.Published, true, data, generation, *valid.Manifest.Label)
				case ingestframe.AudioAddendum:
					require.Empty(t, tail, "EOS is delivered once")
					tail = data
					before := main.BufferedSegments(ms.Streamer())
					require.NoError(t, main.ReceiveAudioAddendum(admitted, ms.Streamer(), data), "mixed-policy encoder packets remain available privately")
					after := main.BufferedSegments(ms.Streamer())
					require.Len(t, after, len(before))
					require.Equal(t, append(bytes.Clone(before[len(before)-1].Data), data...), after[len(after)-1].Data)
					require.False(t, after[len(after)-1].Published, "a public clip must not include this mixed-policy tail")
				default:
					t.Fatalf("unexpected worker frame %s", typ)
				}
				got = append(got, canonicalAudioPackets(t, ctx, data, "opus")...)
			}
			require.NotEmpty(t, tail)
			select {
			case notification := <-notifications:
				require.True(t, notification.AudioOnly)
				require.Equal(t, tail, notification.Muxl)
				require.False(t, notification.Metadata.Published)
			case <-time.After(time.Second):
				t.Fatal("worker EOS audio was not queued for private delivery")
			}
			require.Len(t, got, len(want), "policy changes must not discard any encoded EOS packets")
			for i := range want {
				require.Equal(t, want[i].data, got[i].data, "packet %d", i)
			}
		})
	}
}

func TestAudioTailRestrictionLowersPublicSourceAndClip(t *testing.T) {
	ctx := t.Context()
	ms := audioSignerForTest(t)
	completed, tail := audioAddendumSourceForTest(t, ms)
	// A derivative's signed restriction can only lower source publication. The
	// original source manifests and earlier derivative chunks remain public.
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
	manifest["assertions"] = append(manifest["assertions"].([]any), map[string]any{
		"label": audioPublicationAssertion, "data": map[string]any{"published": false},
	})
	definition, err := json.Marshal(manifest)
	require.NoError(t, err)
	cert, key := nodeSignerForTest(t)
	tail, err = muxl.RunMuxlSignTranscode(ctx, muxl.TranscodeInput{Output: tail, Source: ingredients, CertPEM: cert, KeyPEM: key, Manifest: definition})
	require.NoError(t, err)
	_, isTail, err := verifiedRenditions(ctx, tail)
	require.NoError(t, err)
	require.True(t, isTail)
	main, _ := publisherAccessForTest(t, ms.Streamer())
	admitted, err := main.admitPublisher(ctx, ms.Streamer())
	require.NoError(t, err)
	generation, err := main.checkPublisherContext(admitted, ms.Streamer())
	require.NoError(t, err)
	for _, segment := range completed {
		valid, err := ValidateMP4MediaC2PA(ctx, segment)
		require.NoError(t, err)
		require.True(t, valid.Meta.Published)
		main.bufferSegment(ms.Streamer(), valid.Meta.StartTime.Time(), true, true, segment, generation, *valid.Manifest.Label)
	}
	before := main.BufferedSegments(ms.Streamer())
	notifications := main.NewSegment()
	require.NoError(t, main.ReceiveAudioAddendum(admitted, ms.Streamer(), tail))
	after := main.BufferedSegments(ms.Streamer())
	require.False(t, after[len(after)-1].Published, "the appended private tail must leave the public clip selection")
	require.True(t, before[len(before)-1].Published, "previous clip snapshots remain immutable")
	select {
	case notification := <-notifications:
		require.False(t, notification.Metadata.Published, "an explicit signed tail restriction survives otherwise public contributors")
	case <-time.After(time.Second):
		t.Fatal("restricted tail was not queued for private delivery")
	}
}
