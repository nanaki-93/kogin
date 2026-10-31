package securityrules

import (
	"go/ast"
	"go/token"
	"strconv"
)

func booleanLiteral(expression ast.Expr, expected bool, scope *securityRuleScope) bool {
	expression = unwrapParentheses(expression)
	name, ok := expression.(*ast.Ident)
	return ok && name.Name == strconv.FormatBool(expected) && scope.resolvesPredeclared(name.Name)
}

func staticString(expression ast.Expr) bool {
	expression = unwrapParentheses(expression)
	if _, ok := stringLiteral(expression); ok {
		return true
	}
	binary, ok := expression.(*ast.BinaryExpr)
	return ok && binary.Op == token.ADD && staticString(binary.X) && staticString(binary.Y)
}

func stringLiteral(expression ast.Expr) (string, bool) {
	expression = unwrapParentheses(expression)
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	return value, err == nil
}

func unwrapParentheses(expression ast.Expr) ast.Expr {
	for {
		parentheses, ok := expression.(*ast.ParenExpr)
		if !ok {
			return expression
		}
		expression = parentheses.X
	}
}
