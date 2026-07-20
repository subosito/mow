package mow_test

import (
	"context"
	"fmt"

	"github.com/subosito/mow"
)

// ExampleRun shows the one-shot library entry point. Options.Chat injects a
// stub LLM here so the example runs offline; drop it (and set OPENAI_API_KEY /
// OPENAI_MODEL) to talk to a real endpoint.
func ExampleRun() {
	res, err := mow.Run(context.Background(), "say hi", mow.Options{
		NoSession: true,
		Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "hi"}, nil
		},
	})
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(res.Text)
	// Output: hi
}
