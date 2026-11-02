package logread

import (
	"bufio"
	"io"
	"strings"
)

// scan drops a trailing partial line, because the daemon may be mid-write. With n greater
// than zero only the last n lines are kept, in a ring buffer, so a file of any size costs n
// lines of memory; with n of zero or less every line is kept.
func scan(r io.Reader, n int) (lines []string, total int, consumed int64, err error) {
	reader := bufio.NewReader(r)
	if n > 0 {
		lines = make([]string, 0, n)
	}

	for {
		line, readErr := reader.ReadString('\n')
		if readErr == io.EOF {
			return lines, total, consumed, nil
		}
		if readErr != nil {
			return nil, 0, 0, readErr
		}

		total++
		consumed += int64(len(line))
		text := strings.TrimSuffix(line, "\n")
		if n <= 0 {
			lines = append(lines, text)
			continue
		}
		if len(lines) < n {
			lines = append(lines, text)
			continue
		}
		copy(lines, lines[1:])
		lines[n-1] = text
	}
}
