package transcoder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"regexp"
	"time"

	"stream.place/streamplace/pkg/aqhttp"
	"stream.place/streamplace/pkg/renditions"
)

const maxSourceBytes = 64 << 20
const maxOutputBytes = 128 << 20
const jobTimeout = 30 * time.Second

var ErrBusy = errors.New("transcoder is busy")
var profileName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func validateProfiles(profiles []renditions.Profile) error {
	if len(profiles) == 0 || len(profiles) > 8 {
		return fmt.Errorf("expected 1 to 8 rendition profiles")
	}
	names := make(map[string]bool, len(profiles))
	for _, p := range profiles {
		if !profileName.MatchString(p.Name) || names[p.Name] {
			return fmt.Errorf("invalid or duplicate rendition name %q", p.Name)
		}
		if p.Width < 2 || p.Width > 4096 || p.Width%2 != 0 || p.Height < 2 || p.Height > 4096 || p.Height%2 != 0 {
			return fmt.Errorf("invalid dimensions for rendition %q", p.Name)
		}
		if p.Bitrate < 1000 || p.Bitrate > 100_000_000 || (p.Profile != "high" && p.Profile != "baseline") {
			return fmt.Errorf("invalid rendition profile %q", p.Name)
		}
		if (p.FPS == 0) != (p.FPSDen == 0) || (p.FPS > 0 && float64(p.FPS)/float64(p.FPSDen) > 240) {
			return fmt.Errorf("invalid frame rate for rendition %q", p.Name)
		}
		names[p.Name] = true
	}
	return nil
}

// Transcode submits a finite media job to an operator-configured endpoint.
// Results are matched by name before returning them in requested ladder order.
func Transcode(ctx context.Context, endpoint string, source []byte, profiles []renditions.Profile) ([][]byte, error) {
	if err := validateProfiles(profiles); err != nil {
		return nil, err
	}
	if len(source) == 0 || len(source) > maxSourceBytes {
		return nil, fmt.Errorf("source must contain 1 to %d bytes", maxSourceBytes)
	}
	var framing bytes.Buffer
	writer := multipart.NewWriter(&framing)
	config, err := writer.CreateFormField("profiles")
	if err != nil {
		return nil, err
	}
	if err := json.NewEncoder(config).Encode(profiles); err != nil {
		return nil, err
	}
	_, err = writer.CreateFormFile("source", "segment.mp4")
	if err != nil {
		return nil, err
	}
	cut := framing.Len()
	if err := writer.Close(); err != nil {
		return nil, err
	}
	frame := framing.Bytes()
	body := io.MultiReader(bytes.NewReader(frame[:cut]), bytes.NewReader(source), bytes.NewReader(frame[cut:]))
	ctx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, err
	}
	request.ContentLength = int64(len(frame)) + int64(len(source))
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := aqhttp.DoTrusted(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("transcode request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		if response.StatusCode == http.StatusServiceUnavailable {
			return nil, fmt.Errorf("%w: %s", ErrBusy, message)
		}
		return nil, fmt.Errorf("transcoder returned %d: %s", response.StatusCode, message)
	}
	contentType, params, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || contentType != "multipart/mixed" || params["boundary"] == "" {
		return nil, fmt.Errorf("transcoder returned invalid multipart response")
	}
	reader := multipart.NewReader(io.LimitReader(response.Body, maxOutputBytes+1), params["boundary"])
	outputs := make([][]byte, len(profiles))
	indices := make(map[string]int, len(profiles))
	for i, p := range profiles {
		indices[p.Name] = i
	}
	total := 0
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read transcoder result: %w", err)
		}
		i, ok := indices[part.FormName()]
		if !ok || outputs[i] != nil {
			return nil, fmt.Errorf("unexpected or duplicate rendition %q", part.FormName())
		}
		data, err := io.ReadAll(io.LimitReader(part, int64(maxOutputBytes-total)+1))
		if err != nil {
			return nil, fmt.Errorf("read rendition %q: %w", part.FormName(), err)
		}
		total += len(data)
		if len(data) == 0 || total > maxOutputBytes {
			return nil, fmt.Errorf("transcoder returned empty or oversized renditions")
		}
		outputs[i] = data
	}
	for i, output := range outputs {
		if len(output) == 0 {
			return nil, fmt.Errorf("transcoder omitted rendition %q", profiles[i].Name)
		}
	}
	return outputs, nil
}

type handlerState struct{ jobs chan struct{} }

func NewHandler(jobs int) (http.Handler, error) {
	if jobs < 1 {
		return nil, fmt.Errorf("jobs must be positive")
	}
	return &handlerState{jobs: make(chan struct{}, jobs)}, nil
}

func (h *handlerState) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/transcode" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	select {
	case h.jobs <- struct{}{}:
		defer func() { <-h.jobs }()
	default:
		http.Error(w, ErrBusy.Error(), http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), jobTimeout)
	defer cancel()
	r.Body = http.MaxBytesReader(w, r.Body, maxSourceBytes+16*1024)
	reader, err := r.MultipartReader()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	config, err := reader.NextPart()
	if err != nil || config.FormName() != "profiles" {
		http.Error(w, "profiles must be the first part", http.StatusBadRequest)
		return
	}
	var profiles []renditions.Profile
	decoder := json.NewDecoder(io.LimitReader(config, 8193))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&profiles); err != nil {
		http.Error(w, "invalid profiles", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		http.Error(w, "unexpected profile data", http.StatusBadRequest)
		return
	}
	if err := validateProfiles(profiles); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "source" {
		http.Error(w, "source must be the second part", http.StatusBadRequest)
		return
	}
	source, err := io.ReadAll(io.LimitReader(part, maxSourceBytes+1))
	if err != nil || len(source) == 0 || len(source) > maxSourceBytes {
		http.Error(w, "empty or oversized source", http.StatusRequestEntityTooLarge)
		return
	}
	if _, err := reader.NextPart(); !errors.Is(err, io.EOF) {
		http.Error(w, "unexpected request part", http.StatusBadRequest)
		return
	}
	body, contentType, err := runJob(ctx, source, profiles)
	if err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(body)
}

func writeOutputs(writer *multipart.Writer, profiles []renditions.Profile, outputs [][]byte) error {
	for i, output := range outputs {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": profiles[i].Name, "filename": profiles[i].Name + ".mp4"}))
		header.Set("Content-Type", "video/mp4")
		part, err := writer.CreatePart(header)
		if err != nil {
			return err
		}
		if _, err := part.Write(output); err != nil {
			return err
		}
	}
	return writer.Close()
}
