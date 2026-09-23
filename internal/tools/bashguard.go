package tools

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// bashRefuse returns a tool-result error if the command is refused by harness
// policy: discarding uncommitted work, or using bash as a file finder.
func bashRefuse(cmd string) string {
	if isDestructiveBash(cmd) {
		return "error: discarding uncommitted work is not allowed " +
			"(git checkout/restore/reset --hard, git clean, rm -rf of project trees). " +
			"Fix compile/test errors in place with edit/write; do not wipe WIP for green."
	}
	if isDiscoverBash(cmd) {
		return "error: use the grep tool for content search, glob for file names. " +
			"Do not bash rg/grep/find/fd/ls — those dumps re-enter history unbounded."
	}
	return ""
}

func isDestructiveBash(cmd string) bool {
	c := strings.ToLower(cmd)
	if strings.Contains(c, "git restore ") || strings.Contains(c, "git restore\t") {
		return true
	}
	if strings.Contains(c, "git checkout --") || strings.Contains(c, "git checkout\t--") {
		return true
	}
	if strings.Contains(c, "git reset --hard") || strings.Contains(c, "git reset --merge") {
		return true
	}
	if strings.Contains(c, "git clean -") {
		return true
	}
	if reDestructiveRM.MatchString(c) {
		if strings.Contains(c, "/tmp/") || strings.Contains(c, "$tmp") || strings.Contains(c, "${tmp") {
			return false
		}
		return true
	}
	return false
}

var reDestructiveRM = regexp.MustCompile(`\brm\s+(-[a-zA-Z]*r[a-zA-Z]*f|-rf|-fr)\b`)

func isDiscoverBash(cmd string) bool {
	if isProductiveBash(cmd) {
		return false
	}
	low := strings.ToLower(strings.Join(strings.Fields(cmd), " "))
	if strings.HasPrefix(low, "cd ") {
		if i := strings.Index(low, "&&"); i > 0 {
			low = strings.TrimSpace(low[i+2:])
		} else if i := strings.Index(low, ";"); i > 0 {
			low = strings.TrimSpace(low[i+1:])
		}
	}
	return looksLikeSearch(low) || looksLikeFind(low) || looksLikeLS(low) ||
		hasCmdToken(low, "fd") || strings.Contains(low, "tree ") || strings.TrimSpace(low) == "tree"
}

func isProductiveBash(cmd string) bool {
	c := strings.ToLower(cmd)
	for _, p := range []string{
		"go test", "go build", "go vet", "go generate", "gofmt -w", "go fmt",
		"cargo test", "cargo build", "npm test", "npm run", "pnpm ", "yarn ",
		"pytest", "python -m pytest",
		"just ", "make ", "task ",
		"git add", "git commit", "git push", "git stash", "git mv",
		"git checkout -b", "git switch -c",
	} {
		if strings.Contains(c, p) {
			return true
		}
	}
	return false
}

func looksLikeLS(low string) bool {
	return slices.Contains(strings.Fields(low), "ls")
}

func looksLikeFind(low string) bool {
	return strings.Contains(low, "find ") || strings.HasPrefix(low, "find ") || low == "find"
}

func looksLikeSearch(low string) bool {
	return hasCmdToken(low, "rg") || hasCmdToken(low, "grep") || hasCmdToken(low, "egrep") || hasCmdToken(low, "fgrep")
}

func hasCmdToken(low, name string) bool {
	for f := range strings.FieldsSeq(low) {
		if strings.ToLower(filepath.Base(f)) == name {
			return true
		}
	}
	return false
}
