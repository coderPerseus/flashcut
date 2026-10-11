package editor

import (
	"os/exec"

	"flashcut/internal/media"
)

// makeTestVideo writes a 12-second test pattern with a tone.
func makeTestVideo(dst string) error {
	ffmpeg, err := media.LookTool("ffmpeg")
	if err != nil {
		return err
	}
	return exec.Command(ffmpeg, "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=12:size=320x240:rate=15",
		"-f", "lavfi", "-i", "sine=duration=12",
		"-c:v", "libx264", "-c:a", "aac", "-shortest", dst).Run()
}
