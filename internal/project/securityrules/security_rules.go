package securityrules

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/nanaki-93/mini-orca/v2/internal/project/insight"
	"github.com/nanaki-93/mini-orca/v2/internal/project/symbols"
)

const (
	MaxSecurityFindings            = 5
	maxSecurityFindings            = MaxSecurityFindings
	SecurityTriageOpen             = "open"
	SecurityVerificationUnverified = "unverified"
)

type IndexFile = symbols.IndexFile
type EngineeringInsight = insight.EngineeringInsight

// SecuritySourceAnchor identifies the source file and line span of a security finding.
type SecuritySourceAnchor struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

// SecurityFinding keeps the user's triage separate from verification evidence.
type SecurityFinding struct {
	ID                 string               `json:"id"`
	Rule               string               `json:"rule"`
	Category           string               `json:"category"`
	Title              string               `json:"title"`
	Anchor             SecuritySourceAnchor `json:"source_anchor"`
	Severity           string               `json:"severity"`
	Confidence         string               `json:"confidence"`
	EvidenceKind       string               `json:"evidence_kind"`
	ObservedCondition  string               `json:"observed_condition"`
	Preconditions      string               `json:"preconditions_or_unknowns"`
	Remediation        string               `json:"remediation"`
	VerificationIdea   string               `json:"verification_idea"`
	CWE                string               `json:"cwe,omitempty"`
	Reference          string               `json:"reference,omitempty"`
	Triage             string               `json:"triage"`
	VerificationState  string               `json:"verification_state"`
	EngineeringInsight *EngineeringInsight  `json:"engineering_insight,omitempty"`
}

// SecurityFindingID excludes model wording, insights, CWE, and references.
func SecurityFindingID(f SecurityFinding) string {
	parts := []string{f.Rule, f.Category, f.Anchor.Path, fmt.Sprint(f.Anchor.StartLine), fmt.Sprint(f.Anchor.EndLine), f.EvidenceKind}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "security:" + hex.EncodeToString(sum[:])
}

// SecurityRuleScanResult is the bounded deterministic output for one file.
type SecurityRuleScanResult struct {
	Findings  []SecurityFinding
	Truncated bool
}

type securityRuleCandidate struct {
	position token.Pos
	finding  SecurityFinding
}

// scanGoCandidates parses Go source and evaluates the AST rules, which
// resolve imports and shadowing that a tree-sitter query cannot.
func scanGoCandidates(indexed IndexFile, source string) ([]ruleCandidate, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, indexed.Path, source, 0)
	if err != nil {
		return nil, fmt.Errorf("parse Go security rules: %w", err)
	}
	visitor := newSecurityRuleVisitor(fset, indexed)
	visitor.walkFile(file)
	sort.SliceStable(visitor.candidates, func(i, j int) bool { return visitor.candidates[i].position < visitor.candidates[j].position })
	candidates := make([]ruleCandidate, 0, len(visitor.candidates))
	for _, candidate := range visitor.candidates {
		candidates = append(candidates, ruleCandidate{line: candidate.finding.Anchor.StartLine, finding: candidate.finding})
	}
	return candidates, nil
}

type securityRuleScope struct {
	parent               *securityRuleScope
	imports              map[string]string
	dotImports           map[string]struct{}
	bound                map[string]struct{}
	tlsConfigs           map[string]struct{}
	tlsConfigCollections map[string]struct{}
}

func newSecurityRuleScope(parent *securityRuleScope) *securityRuleScope {
	return &securityRuleScope{parent: parent, bound: make(map[string]struct{}), tlsConfigs: make(map[string]struct{}), tlsConfigCollections: make(map[string]struct{})}
}

func (s *securityRuleScope) bindTLSConfigCollection(name string) {
	s.bind(name)
	s.tlsConfigCollections[name] = struct{}{}
}

func (s *securityRuleScope) bindTLSConfig(name string) {
	s.bind(name)
	s.tlsConfigs[name] = struct{}{}
}

func (s *securityRuleScope) isTLSConfigCollection(name string) bool {
	for current := s; current != nil; current = current.parent {
		if _, bound := current.bound[name]; bound {
			_, isTLSConfigCollection := current.tlsConfigCollections[name]
			return isTLSConfigCollection
		}
	}
	return false
}

func (s *securityRuleScope) isTLSConfig(name string) bool {
	for current := s; current != nil; current = current.parent {
		if _, bound := current.bound[name]; bound {
			_, isTLSConfig := current.tlsConfigs[name]
			return isTLSConfig
		}
	}
	return false
}

func (s *securityRuleScope) bind(name string) {
	if name != "" && name != "_" {
		s.bound[name] = struct{}{}
	}
}

func (s *securityRuleScope) resolvesPredeclared(name string) bool {
	for current := s; current != nil; current = current.parent {
		if _, bound := current.bound[name]; bound {
			return false
		}
		if _, imported := current.imports[name]; imported {
			return false
		}
	}
	return true
}

func (s *securityRuleScope) importsPath(name, wanted string) bool {
	for current := s; current != nil; current = current.parent {
		if imported, ok := current.imports[name]; ok {
			return imported == wanted
		}
		if _, shadowed := current.bound[name]; shadowed {
			return false
		}
	}
	return false
}

func (s *securityRuleScope) importsDotPath(name, wanted string) bool {
	for current := s; current != nil; current = current.parent {
		if _, shadowed := current.bound[name]; shadowed {
			return false
		}
		if _, imported := current.dotImports[wanted]; imported {
			return true
		}
	}
	return false
}

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

func bindReceiverTypeParameterNames(scope *securityRuleScope, fields *ast.FieldList) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		bindReceiverTypeParameters(scope, field.Type)
	}
}

func bindReceiverTypeParameters(scope *securityRuleScope, expression ast.Expr) {
	switch expression := expression.(type) {
	case *ast.ParenExpr:
		bindReceiverTypeParameters(scope, expression.X)
	case *ast.StarExpr:
		bindReceiverTypeParameters(scope, expression.X)
	case *ast.IndexExpr:
		if name, ok := expression.Index.(*ast.Ident); ok {
			scope.bind(name.Name)
		}
	case *ast.IndexListExpr:
		for _, index := range expression.Indices {
			if name, ok := index.(*ast.Ident); ok {
				scope.bind(name.Name)
			}
		}
	}
}

func bindTypeParameterNames(scope *securityRuleScope, fields *ast.FieldList) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		for _, name := range field.Names {
			scope.bind(name.Name)
		}
	}
}

func bindFieldList(scope *securityRuleScope, fields *ast.FieldList) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		for _, name := range field.Names {
			if isTLSConfigType(field.Type, scope) {
				scope.bindTLSConfig(name.Name)
			} else if isTLSConfigCollectionType(field.Type, scope) {
				scope.bindTLSConfigCollection(name.Name)
			} else {
				scope.bind(name.Name)
			}
		}
	}
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

func bindRangeName(scope *securityRuleScope, expression ast.Expr) {
	if name, ok := expression.(*ast.Ident); ok {
		scope.bind(name.Name)
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

func (v *securityRuleVisitor) addFinding(finding SecurityFinding, position token.Pos) {
	line := v.fset.Position(position).Line
	finding.Anchor = SecuritySourceAnchor{Path: v.indexed.Path, StartLine: line, EndLine: line}
	finding.Triage = SecurityTriageOpen
	finding.VerificationState = SecurityVerificationUnverified
	finding.ID = SecurityFindingID(finding)
	v.candidates = append(v.candidates, securityRuleCandidate{position: position, finding: finding})
}
