package media

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/muxl"
)

func TestAudioTailBindsToLateSubscribersCompletedSource(t *testing.T) {
	ctx := t.Context()
	signer := audioSignerForTest(t)
	completed, tail := audioAddendumSourceForTest(t, signer)
	require.GreaterOrEqual(t, len(completed), 2)
	source := completed[len(completed)-1]
	valid, err := ValidateMP4MediaC2PA(ctx, source)
	require.NoError(t, err)
	for _, public := range []bool{false, true} {
		t.Run(fmt.Sprintf("public=%t", public), func(t *testing.T) {
			mm := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
			did := signer.Streamer()
			// Late replay supplies the last completed source, including its
			// verified codec completion, without the earlier standalone sources.
			mm.bufferSegment(did, valid.Meta.StartTime.Time(), public, false, source, 0, *valid.Manifest.Label)
			notifications := mm.NewSegment()
			require.NoError(t, mm.ReceiveAudioAddendum(t.Context(), did, tail))
			select {
			case notification := <-notifications:
				require.True(t, notification.AudioOnly)
				require.Equal(t, *valid.Manifest.Label, notification.Segment.ID)
				require.Equal(t, public, notification.Metadata.Published)
				require.Equal(t, tail, notification.Muxl)
			case <-time.After(time.Second):
				t.Fatal("the admitted EOS tail was not queued")
			}
			buffered := mm.BufferedSegments(did)
			require.Len(t, buffered, 1)
			require.Equal(t, append(bytes.Clone(source), tail...), buffered[0].Data)
			require.Equal(t, public, buffered[0].Published)
		})
	}
	t.Run("local source still requires original history", func(t *testing.T) {
		mm := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
		did := signer.Streamer()
		mm.bufferSegment(did, valid.Meta.StartTime.Time(), true, true, source, 0, *valid.Manifest.Label)
		require.ErrorIs(t, mm.ReceiveAudioAddendum(t.Context(), did, tail), ErrAudioSourceMissing)
		require.Equal(t, source, mm.BufferedSegments(did)[0].Data)
	})
}

func TestLateSubscriberAudioTailRejectsChangedCompletionAuthority(t *testing.T) {
	ctx := t.Context()
	signer := audioSignerForTest(t)
	sources := allSignedBareSegments(t, ctx, signer, getFixture("ertmp-red-h264.mp4"))
	cert, key := nodeSignerForTest(t)
	encoder := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
	var completed [][]byte
	var tail, ingredients []byte
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
		events, err := unwrapMuxlEvents(ctx, source)
		require.NoError(t, err)
		audio, err := sourceAudioIngredient(events)
		require.NoError(t, err)
		ingredients = append(ingredients, audio.data...)
	}
	require.NoError(t, tr.Close())
	require.NotEmpty(t, tail)
	source := completed[len(completed)-1]
	valid, err := ValidateMP4MediaC2PA(ctx, source)
	require.NoError(t, err)
	otherSource := allSignedBareSegments(t, ctx, audioSignerForTest(t), getFixture("ertmp-red-h264.mp4"))[0]
	otherEvents, err := unwrapMuxlEvents(ctx, otherSource)
	require.NoError(t, err)
	otherAudio, err := sourceAudioIngredient(otherEvents)
	require.NoError(t, err)
	unknownCert, unknownKey := nodeSignerForTest(t)
	for _, test := range []struct {
		name              string
		source, cert, key []byte
	}{
		{"another node certificate", ingredients, unknownCert, unknownKey},
		{"another source ingredient", append(bytes.Clone(ingredients), otherAudio.data...), cert, key},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed, err := muxl.RunMuxlSignTranscode(ctx, muxl.TranscodeInput{
				Output: tail, Source: test.source, CertPEM: test.cert, KeyPEM: test.key,
				Manifest: transcodeManifestFor("did:web:node.test", audioTailTitle),
			})
			require.NoError(t, err)
			_, isTail, err := verifiedRenditions(ctx, changed)
			require.NoError(t, err, "the changed continuation still has a valid signature")
			require.True(t, isTail)
			mm := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
			did := signer.Streamer()
			mm.bufferSegment(did, valid.Meta.StartTime.Time(), true, false, source, 0, *valid.Manifest.Label)
			notifications := mm.NewSegment()
			require.ErrorIs(t, mm.ReceiveAudioAddendum(ctx, did, changed), ErrAudioSourceMissing)
			require.Equal(t, source, mm.BufferedSegments(did)[0].Data)
			select {
			case <-notifications:
				t.Fatal("an unbound continuation was queued")
			default:
			}
		})
	}
}

func TestLateSubscriberAudioTailAcceptsPrunedCompletionIngredients(t *testing.T) {
	for _, counts := range [][]int{{16, 16, -1}, {12, 12, 12, -1}} {
		t.Run(fmt.Sprintf("sources=%d", len(counts)), func(t *testing.T) {
			ctx := t.Context()
			signer := audioSignerForTest(t)
			sources := audioSourcePartsForTest(t, ctx, signer, "ertmp-red-h264.mp4", counts)
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
			require.Len(t, completed, len(sources))
			require.NotEmpty(t, tail)
			source := completed[len(completed)-1]
			out, err := muxl.RunMuxlVerify(ctx, bytes.NewReader(source))
			require.NoError(t, err)
			assets, err := parseVerifiedSegments([]byte(out))
			require.NoError(t, err)
			tails, isTail, err := verifiedRenditions(ctx, tail)
			require.NoError(t, err)
			require.True(t, isTail)
			var completionLabels []string
			for _, asset := range assets {
				if asset.TrackID == tails[0].TrackID {
					require.Equal(t, tails[0].Cert, asset.Cert)
					for _, ingredient := range asset.Manifest.Ingredients {
						require.NotNil(t, ingredient.ActiveManifest)
						completionLabels = append(completionLabels, *ingredient.ActiveManifest)
					}
				}
			}
			var tailLabels []string
			for _, ingredient := range tails[0].Manifest.Ingredients {
				require.NotNil(t, ingredient.ActiveManifest)
				tailLabels = append(tailLabels, *ingredient.ActiveManifest)
			}
			t.Logf("last completion ingredients=%d, EOS ingredients=%d", len(completionLabels), len(tailLabels))
			require.Greater(t, len(completionLabels), len(tailLabels), "normal publication and EOS must prune the real encoder's contributors independently")
			require.Subset(t, completionLabels, tailLabels)
			valid, err := ValidateMP4MediaC2PA(ctx, source)
			require.NoError(t, err)
			for _, public := range []bool{false, true} {
				t.Run(fmt.Sprintf("public=%t", public), func(t *testing.T) {
					mm := NewOffline(&config.CLI{BroadcasterHost: "node.test"})
					did := signer.Streamer()
					mm.bufferSegment(did, valid.Meta.StartTime.Time(), public, false, source, 0, *valid.Manifest.Label)
					notifications := mm.NewSegment()
					require.NoError(t, mm.ReceiveAudioAddendum(ctx, did, tail), "a late peer must accept EOS referencing a strict subset of its already admitted completion's inputs")
					select {
					case notification := <-notifications:
						require.True(t, notification.AudioOnly)
						require.Equal(t, *valid.Manifest.Label, notification.Segment.ID)
						require.Equal(t, public, notification.Metadata.Published)
						require.Equal(t, tail, notification.Muxl)
					case <-time.After(time.Second):
						t.Fatal("the admitted EOS tail was not queued")
					}
					buffered := mm.BufferedSegments(did)
					require.Len(t, buffered, 1)
					require.Equal(t, public, buffered[0].Published)
					require.Equal(t, append(bytes.Clone(source), tail...), buffered[0].Data)
				})
			}
		})
	}
}
