package review

import (
	"fmt"
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
	}
	return b.String()
}

// candidateContract is the literal JSON shape requested from the model.
func candidateContract(prof *Profile) string {
	extra := ""
	if prof.Name == "security" {
		extra = `,
      "attack_surface": "how the input reaches this code (omit if unknown)",
      "trust_boundary": "which boundary is crossed (omit if unknown)"`
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
      "evidence": "what in the code shows this is real; name the functions/values involved",
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
- Output the JSON object alone.`
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
	b.WriteString("\nUse read/glob/grep to check the cited code before ruling. If the cited path or line does not contain what the candidate claims, reject it.\n")
	b.WriteString("\n## Candidates\n\n")
	for _, f := range cands {
		b.WriteString(candidateDigest(f))
	}
	b.WriteString("\n## Required output\n\n")
	b.WriteString("Reply with exactly this JSON object, one verdict per candidate id:\n\n")
	b.WriteString(`{
  "verdicts": [
    {
      "id": "the candidate id, copied exactly",
      "status": "confirmed|rejected|uncertain",
      "severity": "optional corrected severity",
      "confidence": "optional corrected confidence",
      "reason": "why it survives, was corrected, or was rejected"
    }
  ],
  "summary": "one sentence on what survived"
}

Rules:
- Emit exactly one verdict per candidate id above; do not invent new ids.
- "confirmed" means the evidence holds and a maintainer should act on it.
- "rejected" means the claim is wrong, already handled elsewhere, out of scope, or a pure nitpick.
- "uncertain" means you could not confirm it from the code available.
- Output the JSON object alone.`)
	return b.String()
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
			"Is there a concrete data or control path from attacker-controlled input to this code?",
			"Is the input actually attacker-controlled, or internal/trusted?",
			"Is validation, escaping, or authorization already applied upstream?",
			"Is the framework or runtime already providing this protection?",
			"Is the affected code reachable in a deployed configuration?",
		}, common...)
	}
	return append([]string{
		"Does the recommendation fit the project's existing conventions?",
		"Is the described behaviour actually wrong, or is it intentional design?",
	}, common...)
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
	b.WriteString("\n")
	return b.String()
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
