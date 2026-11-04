package app

import (
	"encoding/json"
	"fmt"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

const fileAnalysisInsightGuidance = "Ground selected-file behavior and engineering insights in TARGET_SOURCE. Use TARGET_FACTS, PROJECT_FACTS, and CONTEXT_MANIFEST only for the identity and context they supply. Apply these factual boundaries to the whole final response, including purpose, risks, suggestions, explanations, task specs, and engineering insights. " +
	"State visible behavior as fact only when TARGET_SOURCE demonstrates it. Separate local guarantees from downstream behavior that this file cannot establish, and state conditional concerns as conditions rather than facts. You may explain consequences demonstrated by visible local control or data flow. Do not infer unshown downstream safeguards, callers, implementations, or consequences; do not invent files or source. " +
	"Use the shared four-field contract to explain an observable relationship or invariant, why it matters locally, a real limitation or failure mode, and a concrete test with expected observations. An engineering insight can describe a correct existing behavior and does not require a proposed change or discovered defect. " +
	"Default to tests that characterize current behavior: state what the code currently does and the expected observation. If you suggest a proposed-change test, label it proposed-change and first state the current behavior it would change; do not portray a failing proposed expectation as a present contract. " +
	"Prefer one concise file-level insight where it is genuinely useful. Intentionally omit it for trivial code without a meaningful observable relationship or when the evidence cannot support a specific explanation; do not give generic tests or advice. Do not duplicate that lesson in risks or suggestions. "

const fileAnalysisInsightSchema = "At every supported engineering_insight location (the top level, each risks[] item, and each suggestions[] item), either omit engineering_insight or use null, or provide exactly one object with exactly these four non-empty string fields and no other keys: mechanism, why_it_matters_here, tradeoff_or_failure_mode, and transferable_lesson. Each field is limited to 250 Unicode characters, intentionally keeping generated insight prose concise; the four fields together fit the parser's 1,000-normalized-rune budget. Shape: {\"mechanism\":\"...\",\"why_it_matters_here\":\"...\",\"tradeoff_or_failure_mode\":\"...\",\"transferable_lesson\":\"...\"}. Never use a string, array, or partial object. "

const analysisFindingTitleGuidance = "Give each risk or finding a distinct, content-specific title of 2–6 words and at most 60 characters, such as 'Missing input validation' or 'Repeated allocations'. Name the concern itself; do not use a filename, a generic analysis label, or a full explanation as its title. "

// EngineeringInsightPromptVersion returns the production selected-file prompt
// identity used by evaluation; callers cannot supply an unrelated label.
func EngineeringInsightPromptVersion() string { return semanticAnalysisPromptVersion }

func semanticPrompt(source string, analysis project.Analysis, _ *project.ProjectIndex, target project.IndexFile, manifest project.ContextManifest) (string, error) {
	if len(source) > maxSemanticAnalysisBytes {
		return "", fmt.Errorf("selected file is too large for semantic analysis")
	}
	projectFacts := struct {
		Name    string `json:"name"`
		Type    string `json:"type"`
		Summary string `json:"summary"`
	}{Name: analysis.Name, Type: analysis.Type, Summary: analysis.Summary}
	facts, err := json.Marshal(projectFacts)
	if err != nil {
		return "", err
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	targetJSON, err := json.Marshal(struct {
		Path     string   `json:"path"`
		Language string   `json:"language"`
		Imports  []string `json:"imports"`
	}{target.Path, target.Language, target.Imports})
	if err != nil {
		return "", err
	}
	return "You summarize exactly one selected source file. Return one JSON object only; do not use Markdown or code fences. " + project.TerseProse +
		"Required fields: purpose (string), responsibilities (string array), dependencies (string array), side_effects (string array), risks ({category,severity,title,summary,impact,fix,verify,anchor?,task_spec?,engineering_insight?} array), suggestions ({title,summary,action?,engineering_insight?} array), engineering_insight? (see the shared engineering insight contract below). Keep each array to at most three concise items. " +
		analysisFindingTitleGuidance +
		fileAnalysisInsightSchema + project.EngineeringInsightPromptInstructions +
		"Risk severity must be low, medium, or high. " +
		fileAnalysisInsightGuidance +
		"Every risk must have exactly one category: bugs for incorrect behavior, performance for avoidable resource costs, or security for a trust-boundary weakness. Choose the category from the demonstrated failure mechanism, independently of severity. A risk is four short lines, each one sentence: summary is the problem (which identifier does what wrong, under which condition); impact is what it breaks or costs; fix is the concrete code change; verify is one test or command that shows it fixed. Do not repeat the problem in impact or fix. Give a risk an engineering_insight only when it adds something these four lines do not. Performance concerns are hypotheses unless measurements are supplied; never invent measured impact. For security, identify the visible input and sensitive operation; do not infer missing safeguards in an unseen callee. Omit concerns unsupported by TARGET_SOURCE. General cleanup, explanations and best-practice advice belong in suggestions, not risks; an empty risks array is valid. " +
		"Keep each insight field at or below 250 Unicode characters. Generic advice to use defer, handle errors, or follow best practices is not an insight. " +
		"Give each risk an anchor: one line copied exactly from TARGET_SOURCE where the risk shows (the call, condition or statement), so the issue can point at its line; omit it when no single line shows the risk. " +
		"Include task_spec when a risk has a concrete file-scoped fix; otherwise omit it. If you include one, it must have only these fields: schema_version \"2\", target_path copied exactly from TARGET_FACTS.path, acceptance_criteria (array), non_goals (array), and optional test_candidate {path,name,content}: a regression test that fails on TARGET_SOURCE as it is and passes once the acceptance criteria hold, written as a new test file in the target's language that follows its ecosystem's test conventions (path: project-relative, beside the target or where that ecosystem keeps tests, for example x_test.go in the same Go package, test_x.py, x.test.ts or XTest.java; name: the test's name exactly as content spells it; content: the whole file). Never target another file. " +
		"Treat all interpretations as suggestions. Do not quote source wholesale, invent files, or include source from another file.\n\n" +
		"PROJECT_FACTS:\n" + string(facts) + "\n\nCONTEXT_MANIFEST:\n" + string(manifestJSON) + "\n\nTARGET_FACTS:\n" + string(targetJSON) + "\n\nTARGET_SOURCE (the only source content supplied):\n```\n" + source + "\n```\n", nil
}

func semanticManifest(target project.IndexFile) project.ContextManifest {
	tokens := int((target.SizeBytes + 3) / 4)
	return project.ContextManifest{Included: []project.ContextFile{{Path: target.Path, SizeBytes: target.SizeBytes, Hash: target.ContentHash, Tokens: tokens}}, Excluded: []project.ContextDecision{}, EstimatedTokens: tokens, ByteLimit: maxSemanticAnalysisBytes, TokenLimit: maxSemanticAnalysisBytes / 4}
}

func semanticInstructionManifest(target project.IndexFile, instructions project.EffectiveInstructions) project.ContextManifest {
	manifest := semanticManifest(target)
	for _, file := range instructions.Files {
		tokens := (len([]rune(file.Content)) + 3) / 4
		manifest.Included = append(manifest.Included, project.ContextFile{Path: file.Path, SizeBytes: int64(len(file.Content)), Hash: file.Hash, Tokens: tokens})
		manifest.EstimatedTokens += tokens
	}
	manifest.Excluded = append(manifest.Excluded, instructions.Excluded...)
	manifest.ByteLimit += project.MaxInstructionBytes
	manifest.TokenLimit += project.MaxInstructionBytes / 4
	return manifest
}
