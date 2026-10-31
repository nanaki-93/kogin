package securityrules

import (
	"go/ast"
	"go/token"
)

func (v *securityRuleVisitor) bindTLSConfigAssignment(statement *ast.AssignStmt, scope *securityRuleScope) {
	if statement.Tok != token.DEFINE {
		return
	}
	for index, expression := range statement.Lhs {
		name, ok := expression.(*ast.Ident)
		if !ok {
			continue
		}
		if index < len(statement.Rhs) && isTLSConfigValue(statement.Rhs[index], scope) {
			scope.bindTLSConfig(name.Name)
		} else if index < len(statement.Rhs) && isTLSConfigCollectionValue(statement.Rhs[index], scope) {
			scope.bindTLSConfigCollection(name.Name)
		} else {
			scope.bind(name.Name)
		}
	}
}

func (v *securityRuleVisitor) isTLSConfigLiteral(literal *ast.CompositeLit, scope *securityRuleScope) bool {
	return isTLSConfigType(literal.Type, scope)
}

func isTLSConfigType(expression ast.Expr, scope *securityRuleScope) bool {
	switch expression := expression.(type) {
	case *ast.StarExpr:
		return isTLSConfigType(expression.X, scope)
	case *ast.ParenExpr:
		return isTLSConfigType(expression.X, scope)
	case *ast.SelectorExpr:
		packageName, ok := expression.X.(*ast.Ident)
		return ok && expression.Sel.Name == "Config" && scope.importsPath(packageName.Name, "crypto/tls")
	case *ast.Ident:
		return expression.Name == "Config" && scope.importsDotPath(expression.Name, "crypto/tls")
	default:
		return false
	}
}

func isTLSConfigCollectionType(expression ast.Expr, scope *securityRuleScope) bool {
	switch expression := expression.(type) {
	case *ast.ParenExpr:
		return isTLSConfigCollectionType(expression.X, scope)
	case *ast.StarExpr:
		return isTLSConfigCollectionType(expression.X, scope)
	case *ast.ArrayType:
		return isTLSConfigType(expression.Elt, scope)
	case *ast.MapType:
		return isTLSConfigType(expression.Value, scope)
	default:
		return false
	}
}

func isTLSConfigValue(expression ast.Expr, scope *securityRuleScope) bool {
	switch expression := expression.(type) {
	case *ast.CompositeLit:
		return isTLSConfigType(expression.Type, scope)
	case *ast.UnaryExpr:
		return expression.Op == token.AND && isTLSConfigValue(expression.X, scope)
	case *ast.ParenExpr:
		return isTLSConfigValue(expression.X, scope)
	default:
		return false
	}
}

func isTLSConfigCollectionValue(expression ast.Expr, scope *securityRuleScope) bool {
	switch expression := expression.(type) {
	case *ast.CompositeLit:
		return isTLSConfigCollectionType(expression.Type, scope)
	case *ast.UnaryExpr:
		return expression.Op == token.AND && isTLSConfigCollectionValue(expression.X, scope)
	case *ast.ParenExpr:
		return isTLSConfigCollectionValue(expression.X, scope)
	default:
		return false
	}
}

func (v *securityRuleVisitor) addTLSAssignments(statement *ast.AssignStmt, scope *securityRuleScope) {
	if statement.Tok != token.ASSIGN {
		return
	}
	for index, left := range statement.Lhs {
		if index >= len(statement.Rhs) || !booleanLiteral(statement.Rhs[index], true, scope) {
			continue
		}
		selector, ok := left.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "InsecureSkipVerify" {
			continue
		}
		if isTLSConfigReceiver(selector.X, scope) {
			v.addTLSAssignmentFinding(left.Pos())
		}
	}
}

func isTLSConfigReceiver(expression ast.Expr, scope *securityRuleScope) bool {
	switch expression := expression.(type) {
	case *ast.ParenExpr:
		return isTLSConfigReceiver(expression.X, scope)
	case *ast.StarExpr:
		return isTLSConfigReceiver(expression.X, scope)
	case *ast.UnaryExpr:
		return expression.Op == token.AND && isTLSConfigReceiver(expression.X, scope)
	case *ast.Ident:
		return scope.isTLSConfig(expression.Name)
	case *ast.IndexExpr:
		return isTLSConfigCollectionReceiver(expression.X, scope)
	case *ast.IndexListExpr:
		return isTLSConfigCollectionReceiver(expression.X, scope)
	default:
		return false
	}
}

func isTLSConfigCollectionReceiver(expression ast.Expr, scope *securityRuleScope) bool {
	switch expression := expression.(type) {
	case *ast.ParenExpr:
		return isTLSConfigCollectionReceiver(expression.X, scope)
	case *ast.StarExpr:
		return isTLSConfigCollectionReceiver(expression.X, scope)
	case *ast.UnaryExpr:
		return expression.Op == token.AND && isTLSConfigCollectionReceiver(expression.X, scope)
	case *ast.Ident:
		return scope.isTLSConfigCollection(expression.Name)
	default:
		return false
	}
}

func hasInsecureSkipVerifyTrue(literal *ast.CompositeLit, scope *securityRuleScope) bool {
	for _, element := range literal.Elts {
		keyValue, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := keyValue.Key.(*ast.Ident)
		if ok && key.Name == "InsecureSkipVerify" && booleanLiteral(keyValue.Value, true, scope) {
			return true
		}
	}
	return false
}

func (v *securityRuleVisitor) addTLSCompositeFinding(position token.Pos) {
	v.addFinding(SecurityFinding{
		Rule: "go.tls.insecure-skip-verify", Category: "transport-security", Title: "TLS certificate verification is disabled",
		Severity: "high", Confidence: "high", EvidenceKind: "rule_match", CWE: "CWE-295", Reference: "https://pkg.go.dev/crypto/tls#Config",
		ObservedCondition: "A crypto/tls Config composite literal sets InsecureSkipVerify to the literal true.",
		Preconditions:     "Reachability and whether this configuration handles untrusted connections are unknown; syntactic review is required.",
		Remediation:       "Keep certificate verification enabled, or document and isolate a narrowly controlled exception.",
		VerificationIdea:  "Exercise the relevant connection against an invalid certificate after confirming intended trust behavior.",
	}, position)
}

func (v *securityRuleVisitor) addTLSAssignmentFinding(position token.Pos) {
	v.addFinding(SecurityFinding{
		Rule: "go.tls.insecure-skip-verify", Category: "transport-security", Title: "TLS certificate verification is disabled",
		Severity: "high", Confidence: "high", EvidenceKind: "rule_match", CWE: "CWE-295", Reference: "https://pkg.go.dev/crypto/tls#Config",
		ObservedCondition: "A statically identified crypto/tls Config field assignment sets InsecureSkipVerify to the literal true.",
		Preconditions:     "Reachability and whether this configuration handles untrusted connections are unknown; syntactic review is required.",
		Remediation:       "Keep certificate verification enabled, or document and isolate a narrowly controlled exception.",
		VerificationIdea:  "Exercise the relevant connection against an invalid certificate after confirming intended trust behavior.",
	}, position)
}
