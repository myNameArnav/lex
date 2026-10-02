package stream

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"lex/internal/store"
)

// Firefox can discard dependent frames when a new MP4 fragment begins in
// the middle of a GOP. Inspect real muxer output, not just the argument list.
func TestMP4FragmentsStartWithVideoKeyframes(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	path := filepath.Join(t.TempDir(), "long-gop.mkv")
	out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=320x180:r=24:d=12",
		"-f", "lavfi", "-i", "sine=f=440:d=12", "-c:v", "libx264", "-preset", "veryfast", "-bf", "3",
		"-g", "120", "-keyint_min", "120", "-sc_threshold", "0", "-c:a", "eac3", "-shortest", path).CombinedOutput()
	if err != nil {
		t.Fatalf("make B-frame fixture: %v %s", err, out)
	}
	f := file(path, store.Stream{Index: 0, Codec: "h264", Width: 320, Height: 180, FrameRate: 24},
		store.Stream{Index: 1, Codec: "eac3", Channels: 1, SampleRate: 48000})
	for _, tc := range []struct {
		name              string
		copyVideo, stage2 bool
	}{{"copy", true, false}, {"software-encode", false, false}, {"hardware-remux-stage", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := store.DefaultConfig()
			cfg.VideoEncoder, cfg.TranscodeThreads, cfg.FragmentMs = "libx264", 1, 1000
			command, err := BuildArgs(cfg, FFInfo{}, f, Params{VideoCopy: tc.copyVideo, Audio: 1, Burn: -1, Bitrate: 1000, Height: 180, Channels: 2})
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("ffmpeg", command.Args...)
			if tc.stage2 {
				// Exercise the hardware pipeline's actual MP4 remux stage
				// with CPU-generated transport stream input; no V4L2 device
				// is needed to verify its fragment boundaries.
				cfg.VideoEncoder = "h264_v4l2m2m"
				hw, err := BuildArgs(cfg, FFInfo{}, f, Params{Audio: 1, Burn: -1, Bitrate: 1000, Height: 180, Channels: 2})
				if err != nil {
					t.Fatal(err)
				}
				transport, err := exec.Command("ffmpeg", "-v", "error", "-i", path, "-c:v", "copy", "-c:a", "aac", "-f", "mpegts", "pipe:1").Output()
				if err != nil {
					t.Fatal(err)
				}
				cmd = exec.Command("ffmpeg", hw.Stage2...)
				cmd.Stdin = bytes.NewReader(transport)
			}
			progress, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer progress.Close()
			cmd.ExtraFiles = []*os.File{progress}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			data, err := cmd.Output()
			if err != nil {
				t.Fatalf("mux: %v %s", err, stderr.String())
			}
			mp4 := filepath.Join(t.TempDir(), "stream.mp4")
			if err := os.WriteFile(mp4, data, 0600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_packets",
				"-show_entries", "packet=pos,flags", "-of", "json", mp4).Output()
			if err != nil {
				t.Fatal(err)
			}
			var probe struct {
				Packets []struct{ Pos, Flags string }
			}
			if err := json.Unmarshal(out, &probe); err != nil {
				t.Fatal(err)
			}
			fragments := 0
			for offset := 0; offset+8 <= len(data); {
				size := int(binary.BigEndian.Uint32(data[offset:]))
				if size == 1 && offset+16 <= len(data) {
					size = int(binary.BigEndian.Uint64(data[offset+8:]))
				}
				if size < 8 || offset+size > len(data) {
					t.Fatalf("invalid MP4 box at %d", offset)
				}
				if string(data[offset+4:offset+8]) == "mdat" {
					for _, packet := range probe.Packets {
						pos, _ := strconv.Atoi(packet.Pos)
						if pos < offset || pos >= offset+size {
							continue
						}
						fragments++
						if !strings.Contains(packet.Flags, "K") {
							t.Errorf("fragment %d starts with a dependent video frame (flags=%q)", fragments, packet.Flags)
						}
						break
					}
				}
				offset += size
			}
			if fragments < 2 {
				t.Fatalf("only %d video fragments; did not exercise a boundary", fragments)
			}
		})
	}
}
