package schedule_test
import (
  "context"
  "sync/atomic"
  "testing"
  "time"
  "github.com/subosito/mow"
  "github.com/subosito/mow/ext/schedule"
)
func TestEveryFiresImmediately(t *testing.T) {
  t.Setenv("MOW_HOME", t.TempDir())
  t.Setenv("OPENAI_API_KEY","k")
  t.Setenv("OPENAI_MODEL","m")
  var n atomic.Int32
  d := &schedule.Daemon{
    Jobs: []schedule.Job{{ID:"t", Every:"1h", Prompt:"hi"}},
    NewEngine: func() (*mow.Engine, error) {
      return mow.New(mow.Options{NoSession:true, Chat: func(ctx context.Context, messages []mow.Message, tools []mow.ToolSpec) (mow.Message, error) {
        n.Add(1)
        return mow.Message{Role:"assistant", Content:"pong"}, nil
      }})
    },
  }
  ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
  defer cancel()
  _ = d.Start(ctx)
  if n.Load() < 1 {
    t.Fatalf("expected immediate fire, got %d", n.Load())
  }
}
