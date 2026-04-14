package adapters

import "strings"

// GenerateProgressBar generates a text-based progress bar.
// width is the total number of block characters (e.g. 10 or 20).
func GenerateProgressBar(progress, width int) string {
	filled := progress * width / 100
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	var sb strings.Builder
	sb.Grow(width * 3) // UTF-8 block chars are 3 bytes
	for i := 0; i < filled; i++ {
		sb.WriteRune('█')
	}
	for i := 0; i < width-filled; i++ {
		sb.WriteRune('░')
	}
	return sb.String()
}
