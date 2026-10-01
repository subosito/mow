package goal

import (
	"strings"
	"testing"
)

func TestHoistGoalEngineFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    string
		wantErr string
	}{
		{name: "subcommand first", args: []string{"run", "--id", "x"}, want: "run --id x"},
		{
			name: "workspace and extra-root before subcommand",
			args: []string{"--workspace", "/repo", "--extra-root", "/other", "--extra-root", "/ro:ro", "run", "--id", "x"},
			want: "run --workspace /repo --extra-root /other --extra-root /ro:ro --id x",
		},
		{
			name: "equals form and bool flag",
			args: []string{"--workspace=/repo", "--allow-write", "--sandbox=none", "plan", "--goal", "ship"},
			want: "plan --workspace=/repo --allow-write --sandbox=none --goal ship",
		},
		{
			name: "flags after subcommand stay",
			args: []string{"run", "--workspace", "/repo", "--extra-root", "/other"},
			want: "run --workspace /repo --extra-root /other",
		},
		{
			name:    "engine flags on new",
			args:    []string{"--workspace", "/repo", "new", "--id", "x"},
			wantErr: "only valid on run and plan",
		},
		{
			name:    "unknown parent flag",
			args:    []string{"--dir", "/tmp", "run"},
			wantErr: "unknown flag",
		},
		{
			name:    "flag without subcommand",
			args:    []string{"--workspace", "/repo"},
			wantErr: "need a subcommand",
		},
		{
			name:    "valued flag missing value",
			args:    []string{"--extra-root"},
			wantErr: "needs a value",
		},
		{name: "help ignores leading noise only when first is not a flag", args: []string{"help"}, want: "help"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := hoistGoalEngineFlags(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err=%v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, " ") != tt.want {
				t.Fatalf("got %q, want %q", strings.Join(got, " "), tt.want)
			}
		})
	}
}
