package ext

import (
	"fmt"
	"os"
	"strings"
)

func init() {
	RegisterCommand(Command{
		Name:    "ext",
		Summary: "List or toggle extension instances (min_turns / on / off)",
		Run:     runExtCmd,
	})
}

func runExtCmd(args []string) int {
	if len(args) == 0 || args[0] == "list" || args[0] == "status" {
		list := ListExtensions(0)
		if len(list) == 0 {
			fmt.Println("No extensions registered.")
			return 0
		}
		fmt.Println("Extensions:")
		for _, info := range list {
			fmt.Printf("  • %-24s (%s) — %s\n", info.Name, info.Kind, info.Status)
		}
		return 0
	}

	action := strings.ToLower(args[0])
	switch action {
	case "on", "enable":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: mow ext on <name>")
			return 1
		}
		target := args[1]
		if SetExtensionEnabled(target, true) {
			fmt.Printf("Enabled extension %q\n", target)
		} else {
			fmt.Fprintf(os.Stderr, "No extension matching %q found\n", target)
			return 1
		}
	case "off", "disable":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: mow ext off <name>")
			return 1
		}
		target := args[1]
		if SetExtensionEnabled(target, false) {
			fmt.Printf("Disabled extension %q\n", target)
		} else {
			fmt.Fprintf(os.Stderr, "No extension matching %q found\n", target)
			return 1
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown ext command: %s (usage: mow ext [list|on|off <name>])\n", args[0])
		return 1
	}
	return 0
}
