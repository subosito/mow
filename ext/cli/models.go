package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/subosito/mow"
	"github.com/subosito/mow/cliutil"
	"github.com/subosito/mow/ext"
)

func init() {
	ext.RegisterCommand(ext.Command{
		Name:    "models",
		Summary: "list catalog models",
		Layer:   "ext",
		Run:     modelsCmd,
	})
}

func modelsCmd(args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		printModelsUsage()
		return 0
	}
	fs := cliutil.NewFlagSet("models")
	chatOnly := fs.Bool("chat", false, "only models suitable for the agent chat loop")
	var ef cliutil.EngineFlags
	ef.Bind(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	filter := strings.TrimSpace(strings.Join(fs.Args(), " "))
	ef.NoSession = true
	opt := ef.OptionsDeferLLM()
	eng, err := mow.NewHarness(opt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow models: %v\n", err)
		return 1
	}
	defer eng.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	listCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	list, err := eng.ListModels(listCtx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow models: %v\n", err)
		return 1
	}
	if *chatOnly {
		list = mow.FilterChatModels(list)
	}
	if filter != "" {
		list = filterModels(list, filter)
	}
	fmt.Print(formatModelCatalog(list, eng.Model(), eng.Wire()))
	return 0
}

func printModelsUsage() {
	fmt.Fprintf(os.Stderr, `mow models — list catalog models

  mow models [filter]
  --chat               chat-loop models only
  --config --model --base-url --workspace

`)
}

func filterModels(list []mow.ModelInfo, filter string) []mow.ModelInfo {
	fl := strings.ToLower(strings.TrimSpace(filter))
	if fl == "" {
		return list
	}
	out := make([]mow.ModelInfo, 0, len(list))
	for _, m := range list {
		if strings.Contains(strings.ToLower(m.ID), fl) {
			out = append(out, m)
			continue
		}
		if strings.Contains(strings.ToLower(m.Wire), fl) {
			out = append(out, m)
			continue
		}
		if strings.Contains(strings.ToLower(strings.Join(m.Wires, " ")), fl) {
			out = append(out, m)
		}
	}
	return out
}

func formatModelCatalog(list []mow.ModelInfo, current, currentWire string) string {
	var b strings.Builder
	n := len(list)
	fmt.Fprintf(&b, "models  %d", n)
	if cur := strings.TrimSpace(current); cur != "" {
		fmt.Fprintf(&b, "  ·  current %s", cur)
	}
	if w := strings.TrimSpace(currentWire); w != "" {
		fmt.Fprintf(&b, "  ·  wire %s", w)
	}
	b.WriteByte('\n')
	if n == 0 {
		b.WriteString("\n  (empty catalog)\n")
		return b.String()
	}

	type row struct {
		mark, id, wire, efforts string
	}
	rows := make([]row, 0, n)
	idW, wireW, effW := utf8.RuneCountInString("MODEL"), utf8.RuneCountInString("WIRE"), utf8.RuneCountInString("EFFORTS")
	for _, m := range list {
		mark := " "
		if current != "" && strings.EqualFold(m.ID, current) {
			mark = "•"
		}
		r := row{
			mark:    mark,
			id:      strings.TrimSpace(m.ID),
			wire:    formatModelWire(m),
			efforts: formatModelEfforts(m),
		}
		if w := utf8.RuneCountInString(r.id); w > idW {
			idW = w
		}
		if w := utf8.RuneCountInString(r.wire); w > wireW {
			wireW = w
		}
		if w := utf8.RuneCountInString(r.efforts); w > effW {
			effW = w
		}
		rows = append(rows, r)
	}
	const maxID, maxWire, maxEff = 48, 52, 40
	if idW > maxID {
		idW = maxID
	}
	if wireW > maxWire {
		wireW = maxWire
	}
	if effW > maxEff {
		effW = maxEff
	}

	b.WriteByte('\n')
	writeModelRow(&b, " ", padRunes("MODEL", idW), padRunes("WIRE", wireW), padRunes("EFFORTS", effW))
	for _, r := range rows {
		writeModelRow(&b, r.mark, padRunes(clipRunes(r.id, idW), idW), padRunes(clipRunes(r.wire, wireW), wireW), padRunes(clipRunes(r.efforts, effW), effW))
	}
	b.WriteString("\n  * default catalog effort    • current model\n")
	return b.String()
}

func writeModelRow(w io.StringWriter, mark, id, wire, efforts string) {
	_, _ = w.WriteString("  ")
	_, _ = w.WriteString(mark)
	_, _ = w.WriteString(" ")
	_, _ = w.WriteString(id)
	_, _ = w.WriteString("  ")
	_, _ = w.WriteString(wire)
	_, _ = w.WriteString("  ")
	_, _ = w.WriteString(efforts)
	_, _ = w.WriteString("\n")
}

func formatModelWire(m mow.ModelInfo) string {
	wire := strings.TrimSpace(m.Wire)
	if wire == "" {
		return "—"
	}
	if len(m.Wires) == 0 {
		return wire
	}
	var extra []string
	for _, w := range m.Wires {
		w = strings.TrimSpace(w)
		if w == "" || strings.EqualFold(w, wire) {
			continue
		}
		extra = append(extra, w)
	}
	if len(extra) == 0 {
		return wire
	}
	return wire + " (+" + strings.Join(extra, ", ") + ")"
}

func formatModelEfforts(m mow.ModelInfo) string {
	if len(m.Efforts) == 0 {
		if d := strings.TrimSpace(m.DefaultEffort); d != "" {
			return d + "*"
		}
		return "—"
	}
	def := strings.TrimSpace(m.DefaultEffort)
	parts := make([]string, 0, len(m.Efforts))
	seen := false
	for _, e := range m.Efforts {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if def != "" && strings.EqualFold(e, def) {
			parts = append(parts, e+"*")
			seen = true
			continue
		}
		parts = append(parts, e)
	}
	if def != "" && !seen {
		parts = append(parts, def+"*")
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, ", ")
}

func padRunes(s string, width int) string {
	n := utf8.RuneCountInString(s)
	if n >= width {
		return s
	}
	return s + strings.Repeat(" ", width-n)
}

func clipRunes(s string, width int) string {
	if utf8.RuneCountInString(s) <= width {
		return s
	}
	if width <= 1 {
		return "…"
	}
	var b strings.Builder
	i := 0
	for _, r := range s {
		if i >= width-1 {
			break
		}
		b.WriteRune(r)
		i++
	}
	b.WriteRune('…')
	return b.String()
}
