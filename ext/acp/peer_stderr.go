package acp

func appendStderrHint(err error, tail string) error {
	if err == nil || tail == "" {
		return err
	}
	return &stderrError{err: err, tail: tail}
}

type stderrError struct {
	err  error
	tail string
}

func (e *stderrError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error() + "\npeer stderr (tail):\n" + e.tail
}

func (e *stderrError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}
