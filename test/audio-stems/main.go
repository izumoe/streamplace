// Publish signed grouped audio through the node's real segment validation path.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strconv"
	"syscall"
	"time"

	"github.com/bluesky-social/indigo/atproto/atcrypto"
	"github.com/mr-tron/base58"
	"golang.org/x/sync/errgroup"
	"stream.place/streamplace/pkg/aqtime"
	"stream.place/streamplace/pkg/config"
	"stream.place/streamplace/pkg/crypto/spkey"
	"stream.place/streamplace/pkg/media"
	"stream.place/streamplace/pkg/muxl"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func fixtureSigner(ctx context.Context, key, did string) (media.MediaSigner, error) {
	if len(key) < 2 || key[0] != 'z' {
		return nil, fmt.Errorf("fixture requires the harness's account stream key")
	}
	decoded, err := base58.Decode(key[1:])
	if err != nil || len(decoded) <= 32 {
		return nil, fmt.Errorf("invalid fixture stream key")
	}
	if string(decoded[32:]) != did {
		return nil, fmt.Errorf("fixture stream key belongs to a different account")
	}
	private, err := atcrypto.ParsePrivateBytesK256(decoded[:32])
	if err != nil {
		return nil, fmt.Errorf("parse fixture signing key: %w", err)
	}
	signer, err := spkey.KeyToSigner(private)
	if err != nil {
		return nil, err
	}
	return media.MakeMediaSigner(ctx, &config.CLI{}, did, signer, nil)
}

func run(ctx context.Context) error {
	endpoint, key, did, fixture := os.Getenv("SERVER_URL"), os.Getenv("E2E_STREAM_KEY"), os.Getenv("ACCOUNT_DID"), os.Getenv("E2E_AUDIO_STEMS_FIXTURE")
	if endpoint == "" || did == "" || fixture == "" {
		return fmt.Errorf("E2E_AUDIO_STEMS_FIXTURE, SERVER_URL and ACCOUNT_DID require the local harness")
	}
	signer, err := fixtureSigner(ctx, key, did)
	if err != nil {
		return err
	}
	signing, err := signer.SigningInput()
	if err != nil {
		return err
	}
	signing.TrackMeta = map[uint32]muxl.TrackMeta{
		4: {AlternateGroup: 1, Label: "Mix"},
		5: {AlternateGroup: 2, Label: "Voice"},
		6: {AlternateGroup: 3, Label: "Music"},
	}
	manifest := func() ([]byte, error) {
		now := aqtime.FromMillis(time.Now().UnixMilli()).String()
		return json.Marshal(map[string]any{
			"title": "e2e audio stems",
			"assertions": []any{
				map[string]any{"label": "c2pa.actions", "data": map[string]any{"actions": []any{map[string]any{"action": "c2pa.created", "when": now}, map[string]any{"action": "c2pa.published", "when": now}}}},
				map[string]any{"label": "cawg.metadata", "data": map[string]any{"@context": map[string]any{"dc": "http://purl.org/dc/elements/1.1/"}, "dc:creator": did, "dc:title": "e2e test stream", "dc:date": now}},
			},
		})
	}
	signing.TrackManifestFn, signing.WrapperManifestFn = manifest, manifest
	group, runCtx := errgroup.WithContext(ctx)
	remux := exec.CommandContext(runCtx, "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-stream_loop", "-1", "-re", "-i", fixture, "-map", "0", "-c", "copy", "-movflags", "frag_keyframe+empty_moov+default_base_moof", "-f", "mp4", "pipe:1")
	remux.Env = []string{"PATH=" + os.Getenv("PATH"), "LD_LIBRARY_PATH="}
	remux.Stderr = os.Stderr
	input, err := remux.StdoutPipe()
	if err != nil {
		return err
	}
	if err = remux.Start(); err != nil {
		return fmt.Errorf("start fixture replay: %w", err)
	}
	events := make(chan *muxl.MuxlEvent, 2)
	group.Go(func() error {
		defer close(events)
		return muxl.RunMuxlSignSegment(runCtx, input, signing, nil, nil, events)
	})
	group.Go(func() error {
		client := &http.Client{Timeout: 15 * time.Second}
		for event := range events {
			if event.Type != "signed-segment" {
				continue
			}
			ids := make([]uint32, 0, len(event.Tracks))
			for id := range event.Tracks {
				n, err := strconv.ParseUint(id, 10, 32)
				if err != nil {
					return err
				}
				ids = append(ids, uint32(n))
			}
			slices.Sort(ids)
			var segment bytes.Buffer
			for _, id := range ids {
				segment.Write(event.Tracks[strconv.FormatUint(uint64(id), 10)])
			}
			if segment.Len() == 0 {
				continue
			}
			if err := postSegment(runCtx, client, endpoint, segment.Bytes()); err != nil {
				return err
			}
		}
		return nil
	})
	group.Go(func() error {
		if err := remux.Wait(); err != nil && runCtx.Err() == nil {
			return fmt.Errorf("fixture replay: %w", err)
		}
		return nil
	})
	return group.Wait()
}

func postSegment(ctx context.Context, client *http.Client, endpoint string, segment []byte) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/api/segment", bytes.NewReader(segment))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "video/mp4")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("post signed fixture: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("signed fixture rejected: HTTP %d", response.StatusCode)
	}
	return nil
}
