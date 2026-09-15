package mcp

import (
	"fmt"
	"strings"
)

func appendStderrHint(err error, tail string) error {
	if err == nil || strings.TrimSpace(tail) == "" {
		return err
	}
	return fmt.Errorf("%w\nmcp stderr (tail):\n%s", err, tail)
}

func aggregateToolText(texts ...string) (string, error) {
	var b strings.Builder
	for _, text := range texts {
		if text == "" {
			continue
		}
		need := len(text)
		if b.Len() > 0 {
			need++
		}
		if b.Len()+need > maxToolOutputBytes {
			return "", fmt.Errorf("mcp tool output exceeds %d bytes", maxToolOutputBytes)
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text)
	}
	return strings.TrimSpace(b.String()), nil
}
