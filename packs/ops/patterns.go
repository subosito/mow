package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext"
)

func init() {
	ext.RegisterTool(patternTool{})
}

// LogPattern declares a regex the tick greps for in a service's logs. When a
// match count reaches Threshold within Window the pattern is alerting
// (candidates for ops_incident with the pattern name as signature).
type LogPattern struct {
	Name      string `yaml:"name"`
	Regex     string `yaml:"regex"`
	Threshold int    `yaml:"threshold"` // default 1
	Window    string `yaml:"window"`    // e.g. "5m"; "" = whole read
	Severity  string `yaml:"severity"`  // info|warn|critical; default warn
}

func (p LogPattern) threshold() int {
	if p.Threshold > 0 {
		return p.Threshold
	}
	return 1
}

func (p LogPattern) severity() string {
	switch strings.ToLower(strings.TrimSpace(p.Severity)) {
	case "info", "warn", "critical":
		return strings.ToLower(strings.TrimSpace(p.Severity))
	case "":
		return "warn"
	default:
		return "warn"
	}
}

func (p LogPattern) windowDur() time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(p.Window))
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// Patterns compile once and are reused across ticks (regexes are operator
// config, not model text).
var (
	patternCache   = map[string]*regexp.Regexp{}
	patternCacheMu sync.Mutex
)

func compilePattern(regex string) (*regexp.Regexp, error) {
	patternCacheMu.Lock()
	defer patternCacheMu.Unlock()
	if re, ok := patternCache[regex]; ok {
		return re, nil
	}
	re, err := regexp.Compile(regex)
	if err != nil {
		return nil, err
	}
	patternCache[regex] = re
	return re, nil
}

// patternResult is one pattern evaluated against one service.
type patternResult struct {
	Name     string
	Severity string
	Matches  int
	Alert    bool
	Err      string // invalid regex or unreadable log
}

// checkServicePatterns reads each service log (bounded by profile caps) and
// counts lines matching each declared pattern.
func checkServicePatterns(p Profile, pack PackConfig, svc Service) []patternResult {
	out := make([]patternResult, 0, len(svc.Patterns))
	for _, pat := range svc.Patterns {
		name := strings.TrimSpace(pat.Name)
		if name == "" {
			continue
		}
		re, err := compilePattern(pat.Regex)
		if err != nil {
			out = append(out, patternResult{Name: name, Err: "invalid regex: " + err.Error()})
			continue
		}
		total := 0
		for _, lp := range svc.Logs {
			lines, err := readLogFile(lp, p.logMaxLines(pack), p.logMaxBytes(pack))
			if err != nil {
				continue // missing logs degrade to zero matches, not tool failure
			}
			for _, ln := range lines {
				if re.MatchString(ln) {
					total++
				}
			}
		}
		out = append(out, patternResult{
			Name: name, Severity: pat.severity(), Matches: total,
			Alert: total >= pat.threshold(),
		})
	}
	return out
}

type patternTool struct{}

func (patternTool) Name() string   { return "ops_log_pattern" }
func (patternTool) ReadOnly() bool { return true }
func (patternTool) Description() string {
	return "Evaluate declared log patterns (regex + threshold) for a service or all services in an ops profile. Lines matching at/over threshold are marked ALERT — open an ops_incident with the pattern name as signature. Args: ops, service (optional; default all services)."
}
func (patternTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"ops":{"type":"string"},"service":{"type":"string"}}}`)
}
func (patternTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	eng := mow.EngineFromContext(ctx)
	if eng == nil {
		return "error: ops tools need the engine context", nil
	}
	var a struct {
		Ops, Service string
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	p, pack, err := loadProfileForTool(eng, a.Ops)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	var services []Service
	if s := strings.TrimSpace(a.Service); s != "" {
		svc, ok := p.service(s)
		if !ok {
			return fmt.Sprintf("error: unknown service %q in ops=%s — ops_services", s, p.Name), nil
		}
		services = []Service{svc}
	} else {
		services = p.Services
	}
	if len(services) == 0 {
		return fmt.Sprintf("ops=%s: no services in config.yaml", p.Name), nil
	}
	var b strings.Builder
	alerts := 0
	for _, svc := range services {
		res := checkServicePatterns(p, pack, svc)
		if len(res) == 0 {
			continue
		}
		for _, r := range res {
			if r.Err != "" {
				fmt.Fprintf(&b, "ERROR service=%s pattern=%s: %s\n", svc.Name, r.Name, r.Err)
				continue
			}
			mark := "ok"
			if r.Alert {
				mark = "ALERT"
				alerts++
			}
			fmt.Fprintf(&b, "%s service=%s pattern=%s severity=%s matches=%d\n",
				mark, svc.Name, r.Name, r.Severity, r.Matches)
		}
	}
	if b.Len() == 0 {
		return fmt.Sprintf("ops=%s: no patterns declared", p.Name), nil
	}
	return fmt.Sprintf("patterns: %d alert(s)\n%s", alerts, strings.TrimRight(b.String(), "\n")), nil
}
