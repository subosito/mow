package session

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// GetToolResultWindow returns a bounded rune slice from a stored tool body
// without loading the entire file into memory.
func (s *Store) GetToolResultWindow(id string, offset, window int) (body string, start, total int, err error) {
	if !toolResultIDPattern.MatchString(id) {
		return "", 0, 0, fmt.Errorf("session: invalid tool result id %q", id)
	}
	if offset < 0 {
		offset = 0
	}
	if window < 0 {
		window = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.ToolDir()
	if dir == "" {
		return "", 0, 0, fmt.Errorf("session: tool result dir unavailable")
	}
	if _, err := os.Lstat(dir); err != nil {
		return "", 0, 0, fmt.Errorf("session: tool result expired or missing")
	}
	path, err := toolResultPath(dir, id)
	if err != nil {
		return "", 0, 0, fmt.Errorf("session: tool result expired or missing")
	}
	f, err := openRegularFileNoFollow(dir, path)
	if err != nil {
		return "", 0, 0, fmt.Errorf("session: tool result expired or missing")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", 0, 0, fmt.Errorf("session: tool result expired or missing")
	}
	if st.Size() > toolResultMaxReadBytes {
		return "", 0, 0, fmt.Errorf("session: tool result exceeds %d byte read limit", toolResultMaxReadBytes)
	}
	return readRuneWindow(io.LimitReader(f, toolResultMaxReadBytes+1), offset, window)
}

func readRuneWindow(r io.Reader, off, win int) (body string, start, total int, err error) {
	br := bufio.NewReader(r)
	var b strings.Builder
	total = 0
	for {
		ru, _, rerr := br.ReadRune()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", 0, 0, rerr
		}
		if total >= off && total < off+win {
			b.WriteRune(ru)
		}
		total++
	}
	start = off
	if off > total {
		start = total
	}
	return b.String(), start, total, nil
}
