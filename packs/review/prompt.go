package review

import (
	"fmt"
	"sort"
	"strings"
)

// systemPrompt is the shared reviewer system text. It fixes the persona,
// the anti-noise rules, and the output contract for both passes.
func systemPrompt(prof *Profile) string {
	var b strings.Builder
	b.WriteString(prof.reviewerRole())
	b.WriteString("\n\nHard rules:\n")
	b.WriteString("- Report only issues you can support with evidence from the code you were shown or read.\n")
	b.WriteString("- Never invent file paths, line numbers, functions, or CVE/CWE ids. If unsure, omit the field.\n")
	b.WriteString("- Cite the file and line where the problem actually is, not where you noticed it.\n")
	b.WriteString("- Prefer few high-quality findings over a long list. An empty findings list is a valid answer.\n")
	b.WriteString("- Do not report style or formatting preferences unless they materially hurt correctness or maintainability.\n")
	b.WriteString("- Do not propose broad rewrites, refactors, or changes unrelated to the reviewed scope.\n")
	b.WriteString("- You are advisory. Never claim the code is proven correct or proven secure.\n")
	b.WriteString("- Respond with a single JSON object and nothing else: no prose, no markdown fence.\n")
	if prof.Name == "security" {
		b.WriteString("\nSecurity evidence rules:\n")
		b.WriteString("- For each finding, reason about source → transform → sink. Name the attacker-controlled entry, the intermediate hops you saw, and the dangerous use.\n")
		b.WriteString("- Before claiming a weakness, check framework protections, middleware, ORM parameterization, auth helpers, and upstream validation that may already mitigate it.\n")
		b.WriteString("- Populate structured evidence fields when you have real information (source, sink, sanitizers_considered, reachability, attacker_prerequisites, evidence_limitations).\n")
		b.WriteString("- Distinguish model-verified from suspected: high confidence only when the code you read supports every hop; medium when the path is incomplete but strongly implied; low when merely suspected.\n")
		b.WriteString("- This is static advisory reading only — never claim exploitation was proven, never invent runtime behaviour you did not see in code.\n")
	}
	b.WriteString("\nSeverity rubric:\n")
	b.WriteString(prof.severityRubric())
	b.WriteString("\nConfidence: high = evidence is conclusive in the code shown; medium = strongly implied but some context is missing; low = plausible but unproven.\n")
	return b.String()
}

// reviewerRole is the persona line for the profile.
func (p *Profile) reviewerRole() string {
	switch p.Name {
	case "security":
		return "You are a meticulous application security reviewer performing an adversarial read of a code change. " +
			"You think in terms of trust boundaries, attacker-controlled input, and what an attacker could do that the author did not intend."
	default:
		return "You are a meticulous senior software engineer performing code review on a change. " +
			"You care about correctness, error handling, tests, API compatibility, concurrency, and whether the change fits the project's existing conventions."
	}
}

// severityRubric keeps severity meaningful and profile-appropriate.
func (p *Profile) severityRubric() string {
	if p.Name == "security" {
		return "- critical: remote unauthenticated compromise, auth bypass, or mass data exposure.\n" +
			"- high: exploitable by a realistic attacker with meaningful impact (privilege escalation, cross-tenant access, injection with real reach).\n" +
			"- medium: real weakness needing preconditions, or defence-in-depth that is clearly missing.\n" +
			"- low: hardening opportunity with limited impact.\n" +
			"- info: observation worth knowing with no direct security impact.\n"
	}
	return "- critical: data loss, corruption, or a guaranteed crash/hang on a normal path.\n" +
		"- high: incorrect behaviour a user will hit, a resource leak, a race, or a breaking API change.\n" +
		"- medium: wrong behaviour in an edge case, missing error handling, or a notable test gap.\n" +
		"- low: maintainability or clarity problem with real future cost.\n" +
		"- info: minor observation; use sparingly.\n"
}

// candidatePrompt builds the discovery-pass user message.
func candidatePrompt(prof *Profile, sc *Scope) string {
	var b strings.Builder
	b.WriteString("# Pass 1 of 2: candidate discovery\n\n")
	b.WriteString(scopeBriefing(sc))
	b.WriteString("\n## Your task\n\n")
	b.WriteString("Identify candidate ")
	b.WriteString(prof.findingNoun())
	b.WriteString(" in the scope below. Use the read/glob/grep tools to check surrounding code, callers, and tests before you commit to a finding — a claim you cannot support will be rejected in pass 2.\n\n")
	if prof.Name == "security" {
		b.WriteString("For every security candidate, prefer a source→transform→sink narrative in evidence, and fill structured evidence fields when known. If you cannot identify an attacker-controlled source or a real sink, lower confidence or omit the finding. Explicitly note framework protections and upstream guards you considered (including when none were found).\n\n")
	}
	b.WriteString("Budget your exploration: the scope content below is usually enough. Read at most a handful of extra files, do not re-read a file you have already seen, and stop exploring as soon as you can justify your findings. Thoroughness beyond that costs the user real time and money.\n\n")
	b.WriteString(taxonomyBriefing(prof))
	b.WriteString("\n")
	b.WriteString(scopeContent(sc))
	b.WriteString("\n## Required output\n\n")
	b.WriteString("Reply with exactly this JSON object:\n\n")
	b.WriteString(candidateContract(prof))
	return b.String()
}

// findingNoun keeps the ask concrete per profile.
func (p *Profile) findingNoun() string {
	if p.Name == "security" {
		return "security weaknesses"
	}
	return "code review findings"
}

// taxonomyBriefing lists the allowed categories and profile extras.
func taxonomyBriefing(prof *Profile) string {
	var b strings.Builder
	b.WriteString("Allowed categories (use the closest match, else \"other\"):\n")
	names := make([]string, 0, len(prof.Categories))
	for _, c := range prof.Categories {
		names = append(names, string(c))
	}
	b.WriteString("  " + strings.Join(names, ", ") + "\n")
	if len(prof.ExtraFields) > 0 {
		b.WriteString("\nOptional extra fields you may add to a finding when you have real information:\n")
		b.WriteString("  " + strings.Join(prof.ExtraFields, ", ") + "\n")
		if prof.Name == "security" {
			b.WriteString(securityEvidenceFieldGuide())
		}
	}
	return b.String()
}

// securityEvidenceFieldGuide documents structured security evidence keys so
// the model fills them with comparable, triage-friendly values.
func securityEvidenceFieldGuide() string {
	return `
Field meanings (all optional; omit rather than invent):
  source — attacker-controlled entry (parameter, header, file, RPC field, env)
  sink — dangerous use (query, exec, path join, serialize, auth decision)
  sanitizers_considered — validation, escaping, authz, or framework guards you checked (or "none found")
  reachability — reachable | conditional | unknown, with a short why
  attacker_prerequisites — auth, role, network position, feature flag, or other preconditions
  evidence_limitations — what you could not confirm from the code available
  attack_surface — how the input reaches this code
  trust_boundary — which boundary is crossed
  exploitability — practical difficulty of abuse given the code shown
  cwe — CWE id only when you are sure; never invent one
`
}

// candidateContract is the literal JSON shape requested from the model.
func candidateContract(prof *Profile) string {
	extra := ""
	if prof.Name == "security" {
		extra = `,
      "source": "attacker-controlled entry point (omit if unknown)",
      "sink": "dangerous use of that data (omit if unknown)",
      "sanitizers_considered": "guards checked, or 'none found' (omit if not assessed)",
      "reachability": "reachable|conditional|unknown — short why (omit if unknown)",
      "attacker_prerequisites": "what an attacker needs (omit if unknown)",
      "evidence_limitations": "what you could not confirm (omit if none)",
      "attack_surface": "how the input reaches this code (omit if unknown)",
      "trust_boundary": "which boundary is crossed (omit if unknown)",
      "exploitability": "practical difficulty given the code shown (omit if unknown)",
      "cwe": "CWE-id only when sure; omit if guessing"`
	}
	evidenceHint := "what in the code shows this is real; name the functions/values involved"
	if prof.Name == "security" {
		evidenceHint = "source → transform → sink narrative; name functions/values and any guard you checked or found missing"
	}
	return `{
  "findings": [
    {
      "title": "one-line summary of the problem",
      "category": "one of the allowed categories",
      "severity": "critical|high|medium|low|info",
      "confidence": "high|medium|low",
      "path": "workspace-relative path, e.g. internal/api/users.go",
      "start_line": 87,
      "end_line": 90,
      "evidence": "` + evidenceHint + `",
      "impact": "what goes wrong in practice",
      "recommendation": "the smallest change that fixes it, in this project's style"` + extra + `
    }
  ],
  "summary": "one sentence on the overall state of the reviewed scope"
}

Rules for this object:
- "findings" must be present; use [] when you found nothing material.
- "path" must be one of the files listed in the scope above.
- "start_line"/"end_line" must be real line numbers in that file; omit them only if you truly cannot locate the code.
- high confidence means the code evidence is conclusive; use medium/low when the claim is only partially supported or suspected.` + securityContractRules(prof) + `
- Output the JSON object alone.`
}

func securityContractRules(prof *Profile) string {
	if prof == nil || prof.Name != "security" {
		return ""
	}
	return `
- Prefer filling source, sink, sanitizers_considered, reachability, attacker_prerequisites, and evidence_limitations when the code supports them.
- Do not invent CWE ids, exploit steps, or runtime behaviour you did not see.
- If framework/upstream protection already covers the sink, prefer omitting the finding over reporting a false positive.`
}

// verifyPrompt builds the verification-pass user message. The verifier only
// rules on ids, so it cannot introduce new findings or rewrite evidence.
func verifyPrompt(prof *Profile, sc *Scope, cands []Finding) string {
	var b strings.Builder
	b.WriteString("# Pass 2 of 2: verification\n\n")
	b.WriteString("A first pass produced the candidate findings below. Your job is to challenge each one and decide whether it survives. Be strict: a false positive costs the user more than a missed low-severity nit.\n\n")
	b.WriteString(scopeBriefing(sc))
	b.WriteString("\n## Challenge each candidate\n\n")
	for _, q := range prof.verificationQuestions() {
		b.WriteString("- " + q + "\n")
	}
	b.WriteString("\nUse the bounded source excerpts below to check each cited location. Use read/glob/grep only when callers or wider context are still needed. If the cited path or line does not contain what the candidate claims, reject it.\n")
	if prof.Name == "security" {
		b.WriteString("For security candidates, re-derive the source→transform→sink path yourself. Confirm or refute structured fields (source, sink, sanitizers_considered, reachability, attacker_prerequisites). Do not treat pass-1 confidence as proof — only code you can see.\n")
	}
	b.WriteString("\n## Candidate source excerpts\n\n")
	b.WriteString(verificationExcerpts(sc, cands))
	b.WriteString("\n## Candidates\n\n")
	for _, f := range cands {
		b.WriteString(candidateDigest(f))
	}
	b.WriteString("\n## Required output\n\n")
	b.WriteString("Reply with exactly this JSON object, one verdict per candidate id:\n\n")
	b.WriteString(verifyContract(prof))
	return b.String()
}

// verifyContract is the literal JSON shape requested from the verifier.
func verifyContract(prof *Profile) string {
	evidenceFields := ""
	evidenceRules := ""
	if prof != nil && prof.Name == "security" {
		evidenceFields = `,
      "evidence_fields": {
        "source": "optional corrected value, or null to clear",
        "sink": "optional corrected value, or null to clear",
        "reachability": "reachable|conditional|unknown — short why, or null to clear"
      }`
		evidenceRules = `
- evidence_fields is optional. Include only fields you are correcting or clearing.
- Allowed keys: source, sink, sanitizers_considered, reachability, attacker_prerequisites, evidence_limitations, attack_surface, trust_boundary, exploitability, cwe.
- Use null to clear a field you refuted; use a string to set the value you verified from code.
- Do not invent new evidence fields; reason about the digests and code only.
- For security: "confirmed" only when the material claim is model-verified from code (source→sink or an equally concrete weakness such as a hardcoded secret). Use "uncertain" when the path is incomplete or only suspected. Use "rejected" when framework/upstream protection already covers it, the input is not attacker-controlled, or the sink is not real.
- Lower confidence when the path is partial even if you confirm a weaker form of the finding.`
	}
	return `{
  "verdicts": [
    {
      "id": "the candidate id, copied exactly",
      "status": "confirmed|rejected|uncertain",
      "severity": "optional corrected severity",
      "confidence": "optional corrected confidence",
      "reason": "why it survives, was corrected, or was rejected"` + evidenceFields + `
    }
  ],
  "summary": "one sentence on what survived"
}

Rules:
- Emit exactly one verdict per candidate id above; do not invent new ids.
- "confirmed" means the evidence holds and a maintainer should act on it.
- "rejected" means the claim is wrong, already handled elsewhere, out of scope, or a pure nitpick.
- "uncertain" means you could not confirm it from the code available.` + evidenceRules + `
- Output the JSON object alone.`
}

// verificationQuestions are the profile-appropriate challenges from the design.
func (p *Profile) verificationQuestions() []string {
	common := []string{
		"Is this actually introduced or affected by the reviewed scope?",
		"Does the cited path and line really contain the code described?",
		"Is the claim supported by code evidence, or is it a guess?",
		"Is the severity justified by the real impact?",
		"Would a maintainer consider this actionable, or is it a nitpick?",
	}
	if p.Name == "security" {
		return append([]string{
			"Is there a concrete data or control path from attacker-controlled input (source) through transforms to a dangerous use (sink)?",
			"Is the input actually attacker-controlled, or internal/trusted?",
			"Were the claimed sanitizers/guards considered correctly — is validation, escaping, or authorization already applied upstream?",
			"Is the framework, ORM, middleware, or runtime already providing this protection?",
			"Is the affected code reachable under realistic attacker prerequisites (auth, role, network, feature flags)?",
			"Does evidence_limitations understate gaps that should make this uncertain rather than confirmed?",
		}, common...)
	}
	return append([]string{
		"Does the recommendation fit the project's existing conventions?",
		"Is the described behaviour actually wrong, or is it intentional design?",
	}, common...)
}

// verificationExcerpts renders a small line-numbered window around each
// candidate location. It avoids repeating the whole scope while giving pass 2
// direct evidence before it decides whether extra tool reads are needed.
func verificationExcerpts(sc *Scope, cands []Finding) string {
	const radius = 20
	var b strings.Builder
	for _, f := range cands {
		fmt.Fprintf(&b, "### %s — %s\n\n", f.ID, f.Path)
		if sc == nil || sc.index == nil {
			b.WriteString("(source excerpt unavailable; use read)\n\n")
			continue
		}
		i, ok := sc.index[f.Path]
		if !ok || i < 0 || i >= len(sc.Files) || sc.Files[i].Content == "" {
			b.WriteString("(full source not in scope briefing; use read)\n\n")
			continue
		}
		lines := strings.Split(strings.TrimSuffix(sc.Files[i].Content, "\n"), "\n")
		if len(lines) == 0 {
			b.WriteString("(empty file)\n\n")
			continue
		}
		start := f.StartLine
		if start < 1 {
			start = 1
		}
		end := f.EndLine
		if end < start {
			end = start
		}
		lo := max(1, start-radius)
		hi := min(len(lines), end+radius)
		width := len(fmt.Sprint(hi))
		b.WriteString("```\n")
		for line := lo; line <= hi; line++ {
			fmt.Fprintf(&b, "%*d| %s\n", width, line, lines[line-1])
		}
		b.WriteString("```\n\n")
	}
	return b.String()
}

// candidateDigest renders one candidate for the verifier.
func candidateDigest(f Finding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s  [%s / %s / %s]\n", f.ID, f.Severity, f.Confidence, f.Category)
	fmt.Fprintf(&b, "- title: %s\n", f.Title)
	fmt.Fprintf(&b, "- location: %s\n", formatLocation(f.Path, f.StartLine, f.EndLine))
	fmt.Fprintf(&b, "- evidence: %s\n", f.Evidence)
	if f.Impact != "" {
		fmt.Fprintf(&b, "- impact: %s\n", f.Impact)
	}
	if f.Recommendation != "" {
		fmt.Fprintf(&b, "- recommendation: %s\n", f.Recommendation)
	}
	// Structured extras (especially security source/sink fields) must reach the
	// verifier so it can challenge them, not only the free-form evidence prose.
	for _, k := range orderedExtraKeys(f.Extra, SecurityEvidenceFields) {
		fmt.Fprintf(&b, "- %s: %s\n", k, f.Extra[k])
	}
	if len(f.Locations) > 1 {
		for _, l := range f.Locations[1:] {
			fmt.Fprintf(&b, "- related (%s): %s\n", l.Role, formatLocation(l.Path, l.StartLine, l.EndLine))
		}
	}
	b.WriteString("\n")
	return b.String()
}

// orderedExtraKeys returns Extra keys with preferred names first (when present),
// then any remaining keys in sorted order for stable digests and text output.
func orderedExtraKeys(extra map[string]string, preferred []string) []string {
	if len(extra) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var keys []string
	for _, k := range preferred {
		if v := strings.TrimSpace(extra[k]); v != "" {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	var rest []string
	for k, v := range extra {
		if seen[k] || strings.TrimSpace(v) == "" {
			continue
		}
		rest = append(rest, k)
	}
	sort.Strings(rest)
	return append(keys, rest...)
}

// formatLocation renders "path:start-end" / "path:line" / "path".
func formatLocation(path string, start, end int) string {
	switch {
	case start > 0 && end > start:
		return fmt.Sprintf("%s:%d-%d", path, start, end)
	case start > 0:
		return fmt.Sprintf("%s:%d", path, start)
	default:
		return path
	}
}
