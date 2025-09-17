package main

import (
	"bytes"
	"fmt"
	"os/exec"
)

// processVideoForFastStart takes the path to a video file and writes a new
// MP4 file with "fast start" (moov atom at the beginning) so it can begin
// playback before fully downloading. It returns the new output file path.
func processVideoForFastStart(filePath string) (string, error) {
	outPath := filePath + ".processing"

	cmd := exec.Command(
		"ffmpeg",
		"-i", filePath,
		"-c", "copy",
		"-movflags", "faststart",
		"-f", "mp4",
		outPath,
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffmpeg faststart failed: %v: %s", err, stderr.String())
	}

	return outPath, nil
}
