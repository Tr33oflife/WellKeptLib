package core

// probe.go — ffprobe / ffmpeg subprocess wrappers.
//
// All media facts (duration, streams, codecs, HDR) come from ffprobe
// JSON output. Embedded text subtitles are extracted with ffmpeg to
// stdout. Both are external binaries configured in Settings; nothing
// here talks to the network.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ffprobeJSON mirrors the subset of ffprobe -show_streams -show_format
// output the app consumes.
type ffprobeJSON struct {
	Streams []struct {
		Index     int               `json:"index"`
		CodecName string            `json:"codec_name"`
		CodecType string            `json:"codec_type"`
		Width     int               `json:"width"`
		Height    int               `json:"height"`
		BitRate   string            `json:"bit_rate"`
		Channels  int               `json:"channels"`
		ColorTrc  string            `json:"color_transfer"`
		ColorSpc  string            `json:"color_space"`
		ColorPri  string            `json:"color_primaries"`
		Tags      map[string]string `json:"tags"`
	} `json:"streams"`
	Format struct {
		Duration string            `json:"duration"`
		Size     string            `json:"size"`
		BitRate  string            `json:"bit_rate"`
		Tags     map[string]string `json:"tags"`
	} `json:"format"`
}

// probeFile runs ffprobe on one file and flattens the result. Errors
// are captured into ProbeResult.Error (a probe failure is a health
// finding, not a scanner crash).
//
// timeoutSec is the per-file ffprobe timeout (Config.ProbeTimeoutSec,
// v0.5.0 — previously a hard-coded 60s literal here). A non-positive
// value falls back to the default so a hand-built Config can never
// mean "no timeout".
func probeFile(ctx context.Context, ffprobePath, path string, timeoutSec int) *ProbeResult {
	pr := &ProbeResult{}
	if ffprobePath == "" {
		pr.Error = "ffprobe not found (set its path in Settings)"
		return pr
	}
	if timeoutSec <= 0 {
		timeoutSec = defaultProbeTimeoutSec
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, ffprobePath, "-v", "error", "-print_format", "json", "-show_streams", "-show_format", path)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		pr.Error = msg
		return pr
	}
	var raw ffprobeJSON
	if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
		pr.Error = "ffprobe output not parseable: " + err.Error()
		return pr
	}
	pr.OK = true
	pr.Duration, _ = strconv.ParseFloat(raw.Format.Duration, 64)
	pr.Size, _ = strconv.ParseInt(raw.Format.Size, 10, 64)
	pr.Bitrate, _ = strconv.ParseInt(raw.Format.BitRate, 10, 64)
	var audioDescs []string
	for _, s := range raw.Streams {
		si := StreamInfo{Index: s.Index, Codec: s.CodecName, Type: s.CodecType, Width: s.Width, Height: s.Height, Channels: s.Channels}
		si.Bitrate, _ = strconv.ParseInt(s.BitRate, 10, 64)
		if s.Tags != nil {
			si.Language = s.Tags["language"]
			si.Title = s.Tags["title"]
		}
		if s.ColorTrc == "smpte2084" || s.ColorTrc == "arib-std-b67" || s.ColorPri == "bt2020" {
			si.HDR = true
		}
		pr.Streams = append(pr.Streams, si)
		switch s.CodecType {
		case "video":
			// Skip attached pictures (cover art) posing as video.
			if s.CodecName == "mjpeg" || s.CodecName == "png" || s.CodecName == "bmp" {
				continue
			}
			if !pr.HasVideo {
				pr.HasVideo = true
				pr.VideoCodec = s.CodecName
				pr.Width, pr.Height = s.Width, s.Height
				pr.HDR = si.HDR
			}
		case "audio":
			ch := ""
			switch s.Channels {
			case 1:
				ch = "1.0"
			case 2:
				ch = "2.0"
			case 6:
				ch = "5.1"
			case 8:
				ch = "7.1"
			default:
				if s.Channels > 0 {
					ch = fmt.Sprintf("%dch", s.Channels)
				}
			}
			audioDescs = append(audioDescs, strings.TrimSpace(s.CodecName+" "+ch))
		}
	}
	pr.AudioDesc = strings.Join(audioDescs, ", ")
	return pr
}

// fillToolPathsBesideExe fills any empty ffprobe/ffmpeg path in cfg
// with a binary found next to the running executable, or in a bin
// subfolder beside it (v0.1.1 — the Windows drop-in setup). Paths
// already set (PATH detection or Settings) always win.
func fillToolPathsBesideExe(cfg *Config) {
	if cfg.FFprobePath != "" && cfg.FFmpegPath != "" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	dirs := []string{filepath.Dir(exe), filepath.Join(filepath.Dir(exe), "bin")}
	find := func(names ...string) string {
		for _, d := range dirs {
			for _, n := range names {
				p := filepath.Join(d, n)
				if info, err := os.Stat(p); err == nil && !info.IsDir() {
					return p
				}
			}
		}
		return ""
	}
	if cfg.FFprobePath == "" {
		cfg.FFprobePath = find("ffprobe.exe", "ffprobe")
	}
	if cfg.FFmpegPath == "" {
		cfg.FFmpegPath = find("ffmpeg.exe", "ffmpeg")
	}
}

// extractEmbeddedSubtitleFn is the extractor the scanner's audit
// uses — a variable so unit tests can stub the ffmpeg call-out (the
// per-file budget test counts calls instead of spawning ffmpeg).
var extractEmbeddedSubtitleFn = extractEmbeddedSubtitle

// imageSubtitleCodecs are subtitle codecs with no text to detect.
var imageSubtitleCodecs = map[string]bool{
	"hdmv_pgs_subtitle": true, "dvd_subtitle": true, "dvb_subtitle": true, "xsub": true,
}

// textSubtitleCodecs are subtitle codecs ffmpeg can convert to SRT.
var textSubtitleCodecs = map[string]bool{
	"subrip": true, "ass": true, "ssa": true, "mov_text": true, "webvtt": true, "text": true,
}

// extractEmbeddedSubtitle pulls one subtitle stream out of a media
// file as SRT text, sampling the first sampleSec seconds of the file.
//
// v0.3.1 — the sample used to be 900 seconds of content with a 120s
// timeout, PER STREAM: on a real library (the user's live test sat
// at 25/2500) a file with several text tracks could hold a probe
// worker for minutes, because the scan counter only ticks after
// every track is extracted. Two minutes of dialogue is hundreds of
// cues — far beyond the few dozen words the stopword language
// detector needs to judge — so the smaller sample loses nothing
// and bounds each extraction to seconds. The 45s timeout is the
// matching hard cap for pathological files / slow disks.
//
// v0.5.0 — sampleSec and timeoutSec are Config.SubtitleSampleSec /
// Config.SubtitleExtractTimeoutSec (the values above are their
// defaults); non-positive values fall back to the defaults.
func extractEmbeddedSubtitle(ctx context.Context, ffmpegPath, mediaPath string, streamIndex, sampleSec, timeoutSec int) ([]string, error) {
	if ffmpegPath == "" {
		return nil, fmt.Errorf("ffmpeg not found (set its path in Settings)")
	}
	if sampleSec <= 0 {
		sampleSec = defaultSubtitleSampleSec
	}
	if timeoutSec <= 0 {
		timeoutSec = defaultSubtitleExtractTimeoutSec
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()
	args := []string{"-v", "error", "-t", strconv.Itoa(sampleSec), "-i", mediaPath, "-map", fmt.Sprintf("0:%d", streamIndex), "-c:s", "srt", "-f", "srt", "-"}
	cmd := exec.CommandContext(cctx, ffmpegPath, args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, fmt.Errorf("ffmpeg extract failed: %s", msg)
	}
	lines, _ := parseSubtitleText(out.Bytes())
	return lines, nil
}
