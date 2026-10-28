package transcoder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"os/exec"
	"strings"
	"time"

	"stream.place/streamplace/pkg/renditions"
)

type jobDiagnostics struct{ bytes.Buffer }

func (b *jobDiagnostics) Write(data []byte) (int, error) {
	n := len(data)
	if remaining := 4096 - b.Len(); remaining > 0 {
		_, _ = b.Buffer.Write(data[:min(n, remaining)])
	}
	return n, nil
}

func runJob(ctx context.Context, source []byte, profiles []renditions.Profile) ([]byte, string, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, "", err
	}
	config, err := json.Marshal(profiles)
	if err != nil {
		return nil, "", err
	}
	cmd := exec.CommandContext(ctx, executable, "-job", string(config))
	cmd.Env = make([]string, 0, 16)
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "TEMP", "TMP", "SystemRoot", "LD_LIBRARY_PATH", "DYLD_LIBRARY_PATH", "DYLD_FALLBACK_LIBRARY_PATH", "GST_PLUGIN_PATH", "GST_PLUGIN_PATH_1_0", "GST_PLUGIN_SYSTEM_PATH", "GST_PLUGIN_SYSTEM_PATH_1_0", "GST_PLUGIN_SCANNER", "GST_REGISTRY", "GST_REGISTRY_1_0", "GST_DEBUG"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdin = bytes.NewReader(source)
	output := renditionBuffer{limit: maxOutputBytes}
	var diagnostics jobDiagnostics
	cmd.Stdout, cmd.Stderr = &output, &diagnostics
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "", fmt.Errorf("transcode job: %w: %s", err, strings.TrimSpace(diagnostics.String()))
	}
	header, body, ok := bytes.Cut(output.Bytes(), []byte("\r\n\r\n"))
	if !ok || len(header) > 256 || !bytes.HasPrefix(header, []byte("Content-Type: multipart/mixed; boundary=")) {
		return nil, "", fmt.Errorf("transcode job returned invalid framing")
	}
	return body, strings.TrimPrefix(string(header), "Content-Type: "), nil
}

// RunJob is the finite child-process entry point. It receives no node keys or
// application state, and emits results only after every rendition succeeds.
func RunJob(ctx context.Context, config string, input io.Reader, output io.Writer) error {
	var profiles []renditions.Profile
	if err := json.Unmarshal([]byte(config), &profiles); err != nil {
		return fmt.Errorf("decode profiles: %w", err)
	}
	if err := validateProfiles(profiles); err != nil {
		return err
	}
	source, err := io.ReadAll(io.LimitReader(input, maxSourceBytes+1))
	if err != nil {
		return err
	}
	if len(source) == 0 || len(source) > maxSourceBytes {
		return fmt.Errorf("empty or oversized source")
	}
	outputs, err := encode(ctx, source, profiles)
	if err != nil {
		return err
	}
	writer := multipart.NewWriter(output)
	if _, err := fmt.Fprintf(output, "Content-Type: multipart/mixed; boundary=%s\r\n\r\n", writer.Boundary()); err != nil {
		return err
	}
	return writeOutputs(writer, profiles, outputs)
}
