package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"os/exec"
)

type ffprobeStream struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type ffprobeResult struct {
	Streams []ffprobeStream `json:"streams"`
}

// getVideoAspectRatio runs ffprobe on the given file and returns a coarse aspect ratio classification.
// It returns one of: "16:9", "9:16", or "other".
func getVideoAspectRatio(filePath string) (string, error) {
	cmd := exec.Command(
		"ffprobe",
		"-v", "error",
		"-print_format", "json",
		"-show_streams",
		filePath,
	)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return "", err
	}

	var result ffprobeResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return "", err
	}
	if len(result.Streams) == 0 {
		return "", errors.New("ffprobe returned no streams")
	}

	// Prefer the first stream that has width and height > 0
	w, h := 0, 0
	for _, s := range result.Streams {
		if s.Width > 0 && s.Height > 0 {
			w, h = s.Width, s.Height
			break
		}
	}
	if w == 0 || h == 0 {
		return "", errors.New("ffprobe did not provide valid width/height")
	}

	ratio := float64(w) / float64(h)
	const (
		ratio169 = 16.0 / 9.0
		ratio916 = 9.0 / 16.0
		tol      = 0.02 // 2% tolerance to account for encodes like 608x1080
	)

	if math.Abs(ratio-ratio169) <= tol {
		return "16:9", nil
	}
	if math.Abs(ratio-ratio916) <= tol {
		return "9:16", nil
	}
	return "other", nil
}
