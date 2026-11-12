package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/abema/go-mp4"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4/seekablebuffer"
	"github.com/go-gst/go-gst/pkg/gst"
	app "github.com/go-gst/go-gst/pkg/gstapp"
	"stream.place/streamplace/pkg/c2patypes"
	"stream.place/streamplace/pkg/muxl"
)

const audioBoundaryEvent = "streamplace-audio-boundary"
const audioTailTitle = "transcoded audio tail"
const audioPublicationAssertion = "place.stream.audio-publication"

type audioPacket struct {
	data          []byte
	pts, duration uint64
}

type audioOutput struct {
	job                   *transcodeJob
	packet                *audioPacket
	header                []byte
	boundary, eos, muxEOS bool
	err                   error
}

type audioIngredient struct {
	data     []byte
	end      uint64
	policy   *SegmentMetadata
	manifest *c2patypes.Manifest
}

// installAudioOutput observes encoded packets directly; the muxer supplies
// only the initialization segment, preserving the encoder's actual codec data.
func installAudioOutput(pipeline gst.Pipeline, emit func(audioOutput) bool) {
	demux := pipeline.GetByName("demux")
	demux.GetStaticPad("sink").AddProbe(gst.PadProbeTypeEventDownstream, func(sink gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		event := info.GetEvent()
		if event == nil {
			return gst.PadProbeOK
		}
		defer gst.UnsafeEventUnref(event)
		if event.GetType() != gst.EventCustomDownstream || event.GetStructure().GetName() != audioBoundaryEvent {
			return gst.PadProbeOK
		}
		// qtdemux does not forward application byte-boundary events. Its chain
		// has already queued the preceding source's audio at this point.
		// Capturing the demux wrapper here would form a native-pad/Go closure cycle.
		parent := sink.GetParentElement()
		if parent == nil {
			emit(audioOutput{err: fmt.Errorf("audio boundary demux is unavailable")})
			return gst.PadProbeDrop
		}
		pad := parent.GetStaticPad("audio_0")
		if pad == nil || !pad.PushEvent(gst.NewEventCustom(gst.EventCustomDownstream, gst.NewStructureEmpty(audioBoundaryEvent))) {
			emit(audioOutput{err: fmt.Errorf("audio boundary could not reach the audio queue")})
		}
		return gst.PadProbeDrop
	})
	pipeline.GetByName("aq").GetStaticPad("src").AddProbe(gst.PadProbeTypeEventDownstream, func(_ gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		event := info.GetEvent()
		if event == nil {
			return gst.PadProbeOK
		}
		defer gst.UnsafeEventUnref(event)
		if event.GetType() == gst.EventCustomDownstream && event.GetStructure().GetName() == audioBoundaryEvent {
			// No queue follows this pad. Earlier buffers have finished the codec
			// chain and reached the packet sink; internal codec lookahead stays live.
			emit(audioOutput{boundary: true})
			return gst.PadProbeDrop
		}
		return gst.PadProbeOK
	})
	pipeline.GetByName("sink").(app.AppSink).SetCallbacks(&app.AppSinkCallbacks{
		NewSample: func(sink app.AppSink) gst.FlowReturn {
			sample := sink.PullSample()
			if sample == nil {
				return gst.FlowEOS
			}
			defer gst.UnsafeSampleUnref(sample)
			buffer := sample.GetBuffer()
			if buffer.PTS() == gst.ClockTimeNone || buffer.Duration() == gst.ClockTimeNone || buffer.Duration() == 0 {
				emit(audioOutput{err: fmt.Errorf("encoded audio has no timestamp or duration")})
				return gst.FlowError
			}
			data, err := copyBufferData(buffer)
			if err != nil {
				emit(audioOutput{err: err})
				return gst.FlowError
			}
			if !emit(audioOutput{packet: &audioPacket{data: data, pts: uint64(buffer.PTS()), duration: uint64(buffer.Duration())}}) {
				return gst.FlowFlushing
			}
			return gst.FlowOK
		},
		EOS: func(app.AppSink) { emit(audioOutput{eos: true}) },
	})
	var init bytes.Buffer
	headerSent := false
	pipeline.GetByName("header").(app.AppSink).SetCallbacks(&app.AppSinkCallbacks{NewSample: func(sink app.AppSink) gst.FlowReturn {
		sample := sink.PullSample()
		if sample == nil {
			return gst.FlowEOS
		}
		defer gst.UnsafeSampleUnref(sample)
		if headerSent {
			return gst.FlowOK
		}
		buffer := sample.GetBuffer()
		data, err := copyBufferData(buffer)
		if err != nil {
			emit(audioOutput{err: err})
			return gst.FlowError
		}
		init.Write(data)
		b := init.Bytes()
		for offset := 0; offset+8 <= len(b); {
			size := int(binary.BigEndian.Uint32(b[offset : offset+4]))
			if size < 8 {
				emit(audioOutput{err: fmt.Errorf("invalid audio initialization box")})
				return gst.FlowError
			}
			if offset+size > len(b) {
				break
			}
			if string(b[offset+4:offset+8]) == "moov" {
				headerSent = true
				emit(audioOutput{header: bytes.Clone(b[:offset+size])})
				init.Reset()
				break
			}
			offset += size
		}
		return gst.FlowOK
	}, EOS: func(app.AppSink) { emit(audioOutput{muxEOS: true}) }})
}

func sourceAudioIngredient(events []*muxl.MuxlEvent) (audioIngredient, error) {
	catalog := catalogFromEvents(events)
	id, ok := audioTrackID(catalog)
	if !ok {
		return audioIngredient{}, fmt.Errorf("audio source has no audio track")
	}
	var scale uint32
	for _, a := range catalog.Audio.Renditions {
		if a.TrackID() == id {
			scale = a.Timescale()
		}
	}
	if scale == 0 {
		return audioIngredient{}, fmt.Errorf("audio source has no timescale")
	}
	key := fmt.Sprint(id)
	var end uint64
	for _, event := range events {
		if len(event.Tracks[key]) > 0 {
			ticks := event.FirstDecodeTimes[key] + event.Durations[key]
			end = max(end, ticks/uint64(scale)*uint64(time.Second)+ticks%uint64(scale)*uint64(time.Second)/uint64(scale))
		}
	}
	return audioIngredient{data: concatTrackAcrossSegments(events, key), end: end}, nil
}

func audioTicks(ns uint64, scale uint32) uint64 {
	return ns/uint64(time.Second)*uint64(scale) + (ns%uint64(time.Second)*uint64(scale)+uint64(time.Second)/2)/uint64(time.Second)
}

// signAudioChunk uses existing MP4 and muxl writers, retaining native codec
// headers and packet order. Each canonical asset receives its own signature.
func (mm *MediaManager) signAudioChunk(ctx context.Context, header []byte, packets []audioPacket, ingredients []audioIngredient, sourceEvents []*muxl.MuxlEvent, signer muxl.SignerInput, manifest []byte) ([]byte, error) {
	boxes, err := mp4.ExtractBoxWithPayload(bytes.NewReader(header), nil, mp4.BoxPath{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()})
	if err != nil {
		return nil, fmt.Errorf("read encoded audio clock: %w", err)
	}
	if len(boxes) != 1 {
		return nil, fmt.Errorf("encoded audio initialization must contain one track")
	}
	scale := boxes[0].Payload.(*mp4.Mdhd).Timescale
	if scale == 0 {
		return nil, fmt.Errorf("encoded audio initialization has no timescale")
	}
	// Remapped source AAC follows the supplied video IDs; allocate Opus
	// after it while leaving node-minted video IDs available.
	var want uint32
	cat := catalogFromEvents(sourceEvents)
	for _, v := range cat.Video.Renditions {
		if v.TrackID() < renditionTrackBase {
			want = max(want, v.TrackID())
		}
	}
	for _, a := range cat.Audio.Renditions {
		if a.TrackID() < renditionTrackBase || a.TrackID() >= SuppliedRenditionTrackBase {
			want = max(want, a.TrackID())
		}
	}
	if want == ^uint32(0) {
		return nil, fmt.Errorf("no source track ID available for encoded audio")
	}
	want++
	if want >= renditionTrackBase && want < SuppliedRenditionTrackBase {
		return nil, fmt.Errorf("no source track ID available for encoded audio")
	}
	samples := make([]*fmp4.Sample, 0, len(packets))
	for i, p := range packets {
		end := p.pts + p.duration
		if i+1 < len(packets) {
			// Like the native muxer, retain real timestamp discontinuities in
			// the sample table rather than shifting the following packets.
			end = packets[i+1].pts
		}
		startTicks, endTicks := audioTicks(p.pts, scale), audioTicks(end, scale)
		if endTicks <= startTicks || endTicks-startTicks > uint64(^uint32(0)) {
			return nil, fmt.Errorf("encoded audio has an invalid sample duration")
		}
		samples = append(samples, &fmp4.Sample{Payload: p.data, Duration: uint32(endTicks - startTicks)})
	}
	var mp4buf seekablebuffer.Buffer
	if _, err := mp4buf.Write(header); err != nil {
		return nil, err
	}
	part := fmp4.Part{Tracks: []*fmp4.PartTrack{{ID: 1, BaseTime: audioTicks(packets[0].pts, scale), Samples: samples}}}
	if err := part.Marshal(&mp4buf); err != nil {
		return nil, fmt.Errorf("marshal audio packets: %w", err)
	}
	canonical, err := muxl.RunMuxlCanonicalize(ctx, mp4buf.Bytes(), map[uint32]uint32{1: want})
	if err != nil {
		return nil, err
	}
	events, err := unwrapMuxlEvents(ctx, canonical)
	if err != nil {
		return nil, err
	}
	var source []byte
	for _, ingredient := range ingredients {
		source = append(source, ingredient.data...)
	}
	var signed []byte
	for _, event := range events {
		output := event.Tracks[fmt.Sprint(want)]
		if len(output) == 0 {
			continue
		}
		piece, err := muxl.RunMuxlSignTranscode(ctx, muxl.TranscodeInput{Output: output, Source: source, CertPEM: signer.CertPEM, KeyPEM: signer.KeyPEM, Sign: signer.Sign, Manifest: manifest})
		if err != nil {
			return nil, err
		}
		signed = append(signed, piece...)
	}
	if len(signed) == 0 {
		return nil, fmt.Errorf("audio canonicalization emitted no assets")
	}
	return signed, nil
}
