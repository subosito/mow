package review

import (
	"flag"
	"strings"
)

// parseArgs parses flags that may appear before, after, or between positional
// paths, and returns the positional arguments.
//
// Go's flag package stops parsing at the first non-flag argument, so
// `mow review ./pkg --no-verify` would otherwise treat "--no-verify" as a path
// and fail with a confusing "no such file" error. Every other CLI in this
// space accepts trailing flags, so the arguments are permuted first: flags are
// collected in order and parsed, and non-flag arguments become the paths.
//
// A literal "--" ends flag parsing; everything after it is a path even if it
// starts with a dash (needed for files whose names begin with "-").
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	flags, paths := permuteArgs(fs, args)
	if err := fs.Parse(flags); err != nil {
		return nil, err
	}
	// Anything fs kept (e.g. after a "--" it consumed) stays positional.
	return append(paths, fs.Args()...), nil
}

// permuteArgs splits args into flag arguments and positional arguments,
// preserving order within each group.
func permuteArgs(fs *flag.FlagSet, args []string) (flags, paths []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			paths = append(paths, args[i+1:]...)
			return flags, paths
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			paths = append(paths, a)
			continue
		}
		flags = append(flags, a)
		// A flag written as "--name value" consumes the next argument, but
		// only when it takes a value and none was attached with "=".
		if strings.Contains(a, "=") {
			continue
		}
		if i+1 < len(args) && needsValue(fs, a) {
			i++
			flags = append(flags, args[i])
		}
	}
	return flags, paths
}

// needsValue reports whether the named flag consumes a following argument.
// Boolean flags do not (`--staged ./pkg` must keep ./pkg as a path).
func needsValue(fs *flag.FlagSet, arg string) bool {
	name := strings.TrimLeft(arg, "-")
	if name == "" {
		return false
	}
	f := fs.Lookup(name)
	if f == nil {
		// Unknown flag: let flag.Parse produce the error rather than guessing
		// how many arguments it eats.
		return false
	}
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return !(ok && bf.IsBoolFlag())
}
