package securityrules

import (
	"go/ast"
	"go/token"
	"path"
	"strconv"
)

type securityRuleVisitor struct {
	fset       *token.FileSet
	indexed    IndexFile
	candidates []securityRuleCandidate
}

func newSecurityRuleVisitor(fset *token.FileSet, indexed IndexFile) *securityRuleVisitor {
	return &securityRuleVisitor{fset: fset, indexed: indexed}
}

func (v *securityRuleVisitor) walkFile(file *ast.File) {
	scope := newSecurityRuleScope(nil)
	scope.imports, scope.dotImports = goSecurityImports(file)
	for _, declaration := range file.Decls {
		switch declaration := declaration.(type) {
		case *ast.GenDecl:
			v.bindDecl(declaration, scope)
		case *ast.FuncDecl:
			if declaration.Recv == nil {
				scope.bind(declaration.Name.Name)
			}
		}
	}
	for _, declaration := range file.Decls {
		switch declaration := declaration.(type) {
		case *ast.FuncDecl:
			v.walkFunc(declaration, scope)
		case *ast.GenDecl:
			v.walkDecl(declaration, scope)
		}
	}
}

func goSecurityImports(file *ast.File) (map[string]string, map[string]struct{}) {
	imports := make(map[string]string)
	dotImports := make(map[string]struct{})
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil || spec.Name != nil && spec.Name.Name == "_" {
			continue
		}
		if spec.Name != nil && spec.Name.Name == "." {
			dotImports[importPath] = struct{}{}
			continue
		}
		name := path.Base(importPath)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		imports[name] = importPath
	}
	return imports, dotImports
}

func (v *securityRuleVisitor) walkFunc(function *ast.FuncDecl, outer *securityRuleScope) {
	if function.Body == nil {
		return
	}
	scope := newSecurityRuleScope(outer)
	bindReceiverTypeParameterNames(scope, function.Recv)
	bindFieldList(scope, function.Recv)
	bindTypeParameterNames(scope, function.Type.TypeParams)
	bindFieldList(scope, function.Type.Params)
	bindFieldList(scope, function.Type.Results)
	v.walkBlock(function.Body, scope)
}

func (v *securityRuleVisitor) walkBlock(block *ast.BlockStmt, parent *securityRuleScope) {
	scope := newSecurityRuleScope(parent)
	for _, statement := range block.List {
		v.walkStmt(statement, scope)
	}
}

func (v *securityRuleVisitor) walkStmt(statement ast.Stmt, scope *securityRuleScope) {
	switch statement := statement.(type) {
	case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
		v.walkControlStmt(statement, scope)
	case *ast.CaseClause, *ast.CommClause:
		v.walkClause(statement, scope)
	default:
		v.walkBasicStmt(statement, scope)
	}
}

func (v *securityRuleVisitor) walkBasicStmt(statement ast.Stmt, scope *securityRuleScope) {
	switch statement := statement.(type) {
	case *ast.BlockStmt:
		v.walkBlock(statement, scope)
	case *ast.ExprStmt:
		v.walkExpr(statement.X, scope)
	case *ast.AssignStmt:
		for _, expression := range statement.Rhs {
			v.walkExpr(expression, scope)
		}
		for _, expression := range statement.Lhs {
			v.walkExpr(expression, scope)
		}
		v.addTLSAssignments(statement, scope)
		v.bindTLSConfigAssignment(statement, scope)
	case *ast.DeclStmt:
		v.walkDecl(statement.Decl, scope)
	case *ast.ReturnStmt:
		for _, expression := range statement.Results {
			v.walkExpr(expression, scope)
		}
	case *ast.GoStmt:
		v.walkExpr(statement.Call, scope)
	case *ast.DeferStmt:
		v.walkExpr(statement.Call, scope)
	case *ast.SendStmt:
		v.walkExpr(statement.Chan, scope)
		v.walkExpr(statement.Value, scope)
	case *ast.LabeledStmt:
		v.walkStmt(statement.Stmt, scope)
	}
}

func (v *securityRuleVisitor) walkControlStmt(statement ast.Stmt, scope *securityRuleScope) {
	switch statement := statement.(type) {
	case *ast.IfStmt:
		child := newSecurityRuleScope(scope)
		if statement.Init != nil {
			v.walkStmt(statement.Init, child)
		}
		v.walkExpr(statement.Cond, child)
		v.walkBlock(statement.Body, child)
		if statement.Else != nil {
			v.walkStmt(statement.Else, child)
		}
	case *ast.ForStmt:
		child := newSecurityRuleScope(scope)
		if statement.Init != nil {
			v.walkStmt(statement.Init, child)
		}
		v.walkExpr(statement.Cond, child)
		v.walkBlock(statement.Body, child)
		if statement.Post != nil {
			v.walkStmt(statement.Post, child)
		}
	case *ast.RangeStmt:
		v.walkExpr(statement.X, scope)
		child := newSecurityRuleScope(scope)
		if statement.Tok == token.DEFINE {
			bindRangeName(child, statement.Key)
			bindRangeName(child, statement.Value)
		}
		v.walkBlock(statement.Body, child)
	case *ast.SwitchStmt:
		v.walkSwitch(statement.Init, statement.Tag, statement.Body.List, scope)
	case *ast.TypeSwitchStmt:
		child := newSecurityRuleScope(scope)
		if statement.Init != nil {
			v.walkStmt(statement.Init, child)
		}
		v.walkStmt(statement.Assign, child)
		v.walkBlock(&ast.BlockStmt{List: statement.Body.List}, child)
	case *ast.SelectStmt:
		v.walkBlock(&ast.BlockStmt{List: statement.Body.List}, scope)
	}
}

func (v *securityRuleVisitor) walkSwitch(init ast.Stmt, tag ast.Expr, body []ast.Stmt, scope *securityRuleScope) {
	child := newSecurityRuleScope(scope)
	if init != nil {
		v.walkStmt(init, child)
	}
	v.walkExpr(tag, child)
	v.walkBlock(&ast.BlockStmt{List: body}, child)
}

func (v *securityRuleVisitor) walkClause(statement ast.Stmt, scope *securityRuleScope) {
	clauseScope := newSecurityRuleScope(scope)
	switch statement := statement.(type) {
	case *ast.CaseClause:
		for _, expression := range statement.List {
			v.walkExpr(expression, clauseScope)
		}
		for _, body := range statement.Body {
			v.walkStmt(body, clauseScope)
		}
	case *ast.CommClause:
		if statement.Comm != nil {
			v.walkStmt(statement.Comm, clauseScope)
		}
		for _, body := range statement.Body {
			v.walkStmt(body, clauseScope)
		}
	}
}

func (v *securityRuleVisitor) walkDecl(declaration ast.Decl, scope *securityRuleScope) {
	gen, ok := declaration.(*ast.GenDecl)
	if !ok {
		return
	}
	for _, spec := range gen.Specs {
		switch spec := spec.(type) {
		case *ast.TypeSpec:
			scope.bind(spec.Name.Name)
		case *ast.ValueSpec:
			for _, expression := range spec.Values {
				v.walkExpr(expression, scope)
			}
			v.bindValueSpec(spec, scope)
		}
	}
}

func (v *securityRuleVisitor) bindDecl(declaration *ast.GenDecl, scope *securityRuleScope) {
	for _, spec := range declaration.Specs {
		switch spec := spec.(type) {
		case *ast.TypeSpec:
			scope.bind(spec.Name.Name)
		case *ast.ValueSpec:
			v.bindValueSpec(spec, scope)
		}
	}
}

func (v *securityRuleVisitor) bindValueSpec(value *ast.ValueSpec, scope *securityRuleScope) {
	for index, name := range value.Names {
		if isTLSConfigType(value.Type, scope) || index < len(value.Values) && isTLSConfigValue(value.Values[index], scope) {
			scope.bindTLSConfig(name.Name)
		} else if isTLSConfigCollectionType(value.Type, scope) || index < len(value.Values) && isTLSConfigCollectionValue(value.Values[index], scope) {
			scope.bindTLSConfigCollection(name.Name)
		} else {
			scope.bind(name.Name)
		}
	}
}

func (v *securityRuleVisitor) walkExpr(expression ast.Expr, scope *securityRuleScope) {
	if expression == nil {
		return
	}
	ast.Inspect(expression, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.FuncLit:
			child := newSecurityRuleScope(scope)
			bindFieldList(child, node.Type.Params)
			bindFieldList(child, node.Type.Results)
			v.walkBlock(node.Body, child)
			return false
		case *ast.CompositeLit:
			if v.isTLSConfigLiteral(node, scope) && hasInsecureSkipVerifyTrue(node, scope) {
				v.addTLSCompositeFinding(node.Pos())
			}
		case *ast.CallExpr:
			v.isDynamicShellCommand(node, scope)
		}
		return true
	})
}

func (v *securityRuleVisitor) addFinding(finding SecurityFinding, position token.Pos) {
	line := v.fset.Position(position).Line
	finding.Anchor = SecuritySourceAnchor{Path: v.indexed.Path, StartLine: line, EndLine: line}
	finding.Triage = SecurityTriageOpen
	finding.VerificationState = SecurityVerificationUnverified
	finding.ID = SecurityFindingID(finding)
	v.candidates = append(v.candidates, securityRuleCandidate{position: position, finding: finding})
}
