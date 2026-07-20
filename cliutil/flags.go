package cliutil

import (
	"flag"
	"fmt"
	"os"
	"reflect"
	"strings"
)

// NewFlagSet writes help to stderr with --long flags (-x for single-char).
// Parsing still accepts both -name and --name.
func NewFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage of %s:\n", name)
		PrintDefaults(fs)
	}
	return fs
}

// PrintDefaults is flag.PrintDefaults with -- for multi-character names.
func PrintDefaults(fs *flag.FlagSet) {
	fs.VisitAll(func(f *flag.Flag) {
		var b strings.Builder
		b.WriteString("  ")
		b.WriteString(flagDash(f.Name))
		b.WriteString(f.Name)
		name, usage := flag.UnquoteUsage(f)
		if len(name) > 0 {
			b.WriteByte(' ')
			b.WriteString(name)
		}
		if b.Len() <= 4 { // "  -x"
			b.WriteByte('\t')
		} else {
			b.WriteString("\n    \t")
		}
		b.WriteString(strings.ReplaceAll(usage, "\n", "\n    \t"))
		if !isZeroValue(f, f.DefValue) {
			fmt.Fprintf(&b, " (default %q)", f.DefValue)
		}
		fmt.Fprint(fs.Output(), b.String(), "\n")
	})
}

func flagDash(name string) string {
	if len(name) == 1 {
		return "-"
	}
	return "--"
}

func isZeroValue(f *flag.Flag, value string) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	typ := reflect.TypeOf(f.Value)
	var z reflect.Value
	if typ.Kind() == reflect.Pointer {
		z = reflect.New(typ.Elem())
	} else {
		z = reflect.Zero(typ)
	}
	return value == z.Interface().(flag.Value).String()
}
