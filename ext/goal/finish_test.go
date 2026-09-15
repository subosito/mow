package goal_test

import (
	"testing"

	"github.com/subosito/mow/ext/goal"
)

func TestParseStatusJSON(t *testing.T) {
	text := "working\n```goal-status\n{\"status\":\"done\",\"summary\":\"ok\"}\n```\n"
	done, fail, _, sum := goal.ParseStatusJSON(text)
	if !done || fail || sum != "ok" {
		t.Fatalf("done=%v fail=%v sum=%q", done, fail, sum)
	}
	text = "```goal-status\n{\"status\":\"failed\",\"reason\":\"nope\"}\n```"
	done, fail, reason, _ := goal.ParseStatusJSON(text)
	if done || !fail || reason != "nope" {
		t.Fatalf("%v %v %q", done, fail, reason)
	}
}
