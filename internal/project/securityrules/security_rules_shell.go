package securityrules

import (
	"go/ast"
	"go/token"
	"strings"
)

func (v *securityRuleVisitor) isDynamicShellCommand(call *ast.CallExpr, scope *securityRuleScope) bool {
	offset, shell, flag, ok := shellCommandCall(call, scope)
	if !ok || staticString(call.Args[offset+2]) {
		return false
	}
	v.addShellFinding(call.Pos(), shell, flag)
	return true
}

func shellCommandCall(call *ast.CallExpr, scope *securityRuleScope) (int, string, string, bool) {
	offset := 0
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		packageName, ok := fun.X.(*ast.Ident)
		if !ok || !scope.importsPath(packageName.Name, "os/exec") {
			return 0, "", "", false
		}
		if fun.Sel.Name == "CommandContext" {
			offset = 1
		} else if fun.Sel.Name != "Command" {
			return 0, "", "", false
		}
	case *ast.Ident:
		if (fun.Name != "Command" && fun.Name != "CommandContext") || !scope.importsDotPath(fun.Name, "os/exec") {
			return 0, "", "", false
		}
		if fun.Name == "CommandContext" {
			offset = 1
		}
	default:
		return 0, "", "", false
	}
	if len(call.Args) < offset+3 {
		return 0, "", "", false
	}
	shell, ok := shellCommand(call.Args[offset])
	if !ok {
		return 0, "", "", false
	}
	flag, ok := shellFlag(shell, call.Args[offset+1])
	return offset, shell, flag, ok
}

func shellCommand(expression ast.Expr) (string, bool) {
	value, ok := stringLiteral(expression)
	if !ok {
		return "", false
	}
	switch value {
	case "sh", "bash", "zsh", "dash", "ksh":
		return "sh", true
	}
	lower := strings.ToLower(value)
	switch lower {
	case "cmd", "cmd.exe":
		return "cmd", true
	case "powershell", "powershell.exe", "pwsh", "pwsh.exe":
		return "powershell", true
	default:
		return "", false
	}
}

func shellFlag(shell string, expression ast.Expr) (string, bool) {
	value, ok := stringLiteral(expression)
	if !ok {
		return "", false
	}
	switch shell {
	case "sh":
		return "-c", value == "-c"
	case "cmd":
		return "/c", strings.EqualFold(value, "/c")
	case "powershell":
		switch strings.ToLower(value) {
		case "-command", "/command", "-c", "/c":
			return value, true
		}
	}
	return "", false
}

func (v *securityRuleVisitor) addShellFinding(position token.Pos, shell, flag string) {
	v.addFinding(SecurityFinding{
		Rule: "go.shell.dynamic-command", Category: "command-injection", Title: "Dynamic shell command string",
		Severity: "high", Confidence: "high", EvidenceKind: "rule_match", CWE: "CWE-78",
		ObservedCondition: shellObservedCondition(shell, flag),
		Preconditions:     "Data origin, shell interpretation, and reachability are unknown; syntactic review is required.",
		Remediation:       "Pass fixed argv elements directly to os/exec and avoid shell parsing of dynamic data.",
		VerificationIdea:  "Trace the command-string inputs and confirm untrusted values cannot alter shell syntax.",
	}, position)
}

func shellObservedCondition(shell, flag string) string {
	switch shell {
	case "sh":
		return "An os/exec sh-family -c invocation receives a non-static command string."
	case "cmd":
		return "An os/exec cmd /c invocation receives a non-static command string."
	default:
		return "An os/exec PowerShell " + flag + " invocation receives a non-static command string."
	}
}
