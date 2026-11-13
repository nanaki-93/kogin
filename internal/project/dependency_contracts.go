package project

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/nanaki-93/kogin/v2/internal/project/projectmap"
	"github.com/nanaki-93/kogin/v2/internal/project/rules"
	"github.com/nanaki-93/kogin/v2/internal/project/syntax"
	"github.com/nanaki-93/kogin/v2/internal/textutil"
)

const (
	// MaxDependencyContractBytes bounds the cross-file contract supplement
	// attached to per-file semantic analysis prompts.
	MaxDependencyContractBytes = 16 * 1024

	// DependencyContractPromptGuidance introduces cross-file contracts for the
	// semantic prompt without overriding source-grounding boundaries.
	DependencyContractPromptGuidance = "DEPENDENCY_CONTRACTS (bounded contracts of in-project symbols referenced by TARGET_SOURCE; verify call arguments, return values, and required state preconditions against these contracts):\n"

	maxContractDeclLines     = 18
	maxContractHelperDepth   = 3
	maxContractHelpers       = 12
	maxPackagePeerSignatures = 10
	maxContractModuleFiles   = 24
	maxContractDocRunes      = 240
)

// DependencyContracts holds bounded cross-file contracts plus provenance for
// inclusion in CONTEXT_MANIFEST.
type DependencyContracts struct {
	Text      string
	Files     []ContextFile
	Excluded  []ContextDecision
	Truncated bool
}

type loadedContractFile struct {
	file    IndexFile
	module  string
	content string
}

type contractEmission struct {
	sections []string
	used     map[string]int
}

// ResolveDependencyContracts extracts bounded contracts and internal call/state
// chains for in-project symbols referenced by targetSource, adhering strictly
// to ContextPolicy and non-test prioritization.
func ResolveDependencyContracts(ctx context.Context, root string, index *ProjectIndex, target IndexFile, targetSource string) (DependencyContracts, error) {
	if index == nil || !projectmap.IsCode(target.Language) {
		return DependencyContracts{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	canonical, err := CanonicalRoot(root)
	if err != nil {
		return DependencyContracts{}, err
	}
	policy, err := NewContextPolicy(canonical)
	if err != nil {
		return DependencyContracts{}, err
	}
	importedByRaw := projectmap.ResolveImports(canonical, index.Files, target)
	sameModule := projectmap.ModuleOf(target.Path)
	byModule, modules, excluded, err := loadContractModules(ctx, canonical, policy, index, target, importedByRaw, sameModule)
	if err != nil {
		return DependencyContracts{}, err
	}
	emission := buildContractEmission(target, targetSource, sameModule, importedByRaw, byModule, modules)
	return finalizeDependencyContracts(emission, byModule, excluded), nil
}

func loadContractModules(ctx context.Context, canonical string, policy *ContextPolicy, index *ProjectIndex, target IndexFile, importedByRaw map[string][]string, sameModule string) (map[string][]loadedContractFile, []string, []ContextDecision, error) {
	modules, moduleSet := collectTargetModules(sameModule, importedByRaw)
	testResolver := rules.NewResolver(canonical)
	perModule := map[string][]IndexFile{}
	for _, file := range index.Files {
		mod := projectmap.ModuleOf(file.Path)
		if !moduleSet[mod] || !eligibleContractIndexFile(file, target.Path, testResolver) {
			continue
		}
		perModule[mod] = append(perModule[mod], file)
	}
	loaded := map[string][]loadedContractFile{}
	var excluded []ContextDecision
	for _, mod := range modules {
		files := perModule[mod]
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
		if len(files) > maxContractModuleFiles {
			files = files[:maxContractModuleFiles]
		}
		modLoaded, modExcluded, err := readContractFiles(ctx, policy, mod, files)
		if err != nil {
			return nil, nil, nil, err
		}
		excluded = append(excluded, modExcluded...)
		if len(modLoaded) > 0 {
			loaded[mod] = modLoaded
		}
	}
	return loaded, modules, excluded, nil
}

func collectTargetModules(sameModule string, importedByRaw map[string][]string) ([]string, map[string]bool) {
	moduleSet := map[string]bool{sameModule: true}
	for _, mods := range importedByRaw {
		for _, mod := range mods {
			moduleSet[mod] = true
		}
	}
	modules := make([]string, 0, len(moduleSet))
	for mod := range moduleSet {
		modules = append(modules, mod)
	}
	sort.Strings(modules)
	return modules, moduleSet
}

func eligibleContractIndexFile(file IndexFile, targetPath string, testResolver *rules.Resolver) bool {
	if file.Path == targetPath || file.Binary || !projectmap.IsCode(file.Language) {
		return false
	}
	if file.SizeBytes > maxFileViewBytes || testResolver.IsTestFile(file.Path) {
		return false
	}
	return true
}

func readContractFiles(ctx context.Context, policy *ContextPolicy, mod string, files []IndexFile) ([]loadedContractFile, []ContextDecision, error) {
	var loaded []loadedContractFile
	var excluded []ContextDecision
	for _, file := range files {
		decision := policy.Decide(file.Path)
		if !decision.Include {
			excluded = append(excluded, decision)
			continue
		}
		data, err := policy.ReadSource(ctx, file.Path, file.ContentHash)
		if err != nil {
			if errors.Is(err, ErrExcludedFile) {
				excluded = append(excluded, decision)
				continue
			}
			if ctx.Err() != nil || errors.Is(err, ErrRevisionConflict) {
				return nil, nil, err
			}
			continue
		}
		loaded = append(loaded, loadedContractFile{file: file, module: mod, content: string(data)})
	}
	return loaded, excluded, nil
}

func buildContractEmission(target IndexFile, targetSource, sameModule string, importedByRaw map[string][]string, byModule map[string][]loadedContractFile, modules []string) contractEmission {
	if len(byModule) == 0 {
		return contractEmission{used: map[string]int{}}
	}
	if strings.EqualFold(filepath.Ext(target.Path), ".go") {
		return buildGoContracts(target.Path, targetSource, sameModule, importedByRaw, byModule, modules)
	}
	return buildGenericContracts(target.Path, targetSource, sameModule, byModule, modules)
}

func finalizeDependencyContracts(emission contractEmission, byModule map[string][]loadedContractFile, excluded []ContextDecision) DependencyContracts {
	text := strings.Join(emission.sections, "\n\n")
	truncated := false
	if len(text) > MaxDependencyContractBytes {
		text = textutil.Truncate(text, MaxDependencyContractBytes)
		truncated = true
	}
	out := DependencyContracts{Text: text, Excluded: excluded, Truncated: truncated}
	if len(emission.used) == 0 || text == "" {
		return out
	}
	fileByPath := map[string]loadedContractFile{}
	for _, files := range byModule {
		for _, f := range files {
			fileByPath[f.file.Path] = f
		}
	}
	paths := make([]string, 0, len(emission.used))
	for path := range emission.used {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		f, ok := fileByPath[path]
		if !ok {
			continue
		}
		out.Files = append(out.Files, ContextFile{
			Path:      f.file.Path,
			SizeBytes: int64(len(f.content)),
			Hash:      f.file.ContentHash,
			Tokens:    (emission.used[path] + 3) / 4,
			Truncated: true,
		})
	}
	return out
}

// --- Go contract extraction ---

type goTargetRefs struct {
	selectors   map[string]map[string]bool
	methods     map[string]bool
	freeIdents  map[string]bool
	aliasToMod  map[string][]string
	dotImported map[string]bool
}

type goContractDecl struct {
	name       string
	baseName   string
	methodName string
	kind       string
	exported   bool
	file       string
	startLine  int
	endLine    int
	doc        string
	signature  string
	snippet    string
	callees    []string
}

type goModuleInfo struct {
	pkgName string
	decls   []goContractDecl
	byName  map[string][]int
	byBase  map[string][]int
	byMeth  map[string][]int
}

func buildGoContracts(targetPath, targetSource, sameModule string, importedByRaw map[string][]string, byModule map[string][]loadedContractFile, modules []string) contractEmission {
	emission := contractEmission{used: map[string]int{}}
	fset := token.NewFileSet()
	targetAST, err := parser.ParseFile(fset, targetPath, targetSource, parser.ParseComments)
	if targetAST == nil || (err != nil && len(targetAST.Decls) == 0) {
		return emission
	}
	parsedMods := parseGoModules(byModule, modules)
	refs := collectGoTargetRefs(targetAST, importedByRaw, parsedMods)
	for _, mod := range modules {
		info := parsedMods[mod]
		if info == nil || len(info.decls) == 0 {
			continue
		}
		section := renderGoModuleSection(mod, mod == sameModule, info, refs, emission.used)
		if section != "" {
			emission.sections = append(emission.sections, section)
		}
	}
	return emission
}

func parseGoModules(byModule map[string][]loadedContractFile, modules []string) map[string]*goModuleInfo {
	out := make(map[string]*goModuleInfo, len(modules))
	for _, mod := range modules {
		files := byModule[mod]
		if len(files) == 0 {
			continue
		}
		info := &goModuleInfo{
			byName: map[string][]int{},
			byBase: map[string][]int{},
			byMeth: map[string][]int{},
		}
		for _, f := range files {
			parseGoModuleFile(f, info)
		}
		out[mod] = info
	}
	return out
}

func parseGoModuleFile(f loadedContractFile, info *goModuleInfo) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, f.file.Path, f.content, parser.ParseComments)
	if parsed == nil || (err != nil && len(parsed.Decls) == 0) {
		return
	}
	if info.pkgName == "" && parsed.Name != nil {
		info.pkgName = parsed.Name.Name
	}
	for _, decl := range parsed.Decls {
		for _, item := range extractGoDecls(f.file.Path, f.content, fset, decl) {
			idx := len(info.decls)
			info.decls = append(info.decls, item)
			info.byName[item.name] = append(info.byName[item.name], idx)
			if item.baseName != "" && item.baseName != item.name {
				info.byBase[item.baseName] = append(info.byBase[item.baseName], idx)
			}
			if item.methodName != "" {
				info.byMeth[item.methodName] = append(info.byMeth[item.methodName], idx)
			}
		}
	}
}

func extractGoDecls(path, content string, fset *token.FileSet, decl ast.Decl) []goContractDecl {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		return []goContractDecl{extractGoFuncDecl(path, content, fset, d)}
	case *ast.GenDecl:
		return extractGoGenDecls(path, content, fset, d)
	default:
		return nil
	}
}

func extractGoFuncDecl(path, content string, fset *token.FileSet, fn *ast.FuncDecl) goContractDecl {
	start := fset.Position(fn.Pos()).Line
	end := fset.Position(fn.End()).Line
	name := fn.Name.Name
	kind, methodName := "function", ""
	if recv := goReceiverTypeName(fn); recv != "" {
		kind = "method"
		name = recv + "." + fn.Name.Name
		methodName = fn.Name.Name
	}
	doc := cleanGoDoc(fn.Doc)
	headerEnd := end
	if fn.Body != nil {
		headerEnd = fset.Position(fn.Body.Lbrace).Line
	}
	sig := cleanGoSignature(sourceLines(content, start, headerEnd))
	return goContractDecl{
		name:       name,
		baseName:   fn.Name.Name,
		methodName: methodName,
		kind:       kind,
		exported:   fn.Name.IsExported(),
		file:       path,
		startLine:  start,
		endLine:    end,
		doc:        doc,
		signature:  sig,
		snippet:    formatGoDeclSnippet(content, doc, sig, start, end, fn.Body == nil),
		callees:    collectGoBodyCallees(fn),
	}
}

func extractGoGenDecls(path, content string, fset *token.FileSet, decl *ast.GenDecl) []goContractDecl {
	if decl.Tok == token.IMPORT {
		return nil
	}
	var out []goContractDecl
	groupDoc := cleanGoDoc(decl.Doc)
	for _, spec := range decl.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			start := fset.Position(s.Pos()).Line
			end := fset.Position(s.End()).Line
			doc := firstNonEmpty(cleanGoDoc(s.Doc), groupDoc)
			sig := "type " + cleanGoSignature(sourceLines(content, start, start))
			out = append(out, goContractDecl{
				name:      s.Name.Name,
				baseName:  s.Name.Name,
				kind:      "type",
				exported:  s.Name.IsExported(),
				file:      path,
				startLine: start,
				endLine:   end,
				doc:       doc,
				signature: sig,
				snippet:   formatGoDeclSnippet(content, doc, sig, start, end, false),
				callees:   collectGoBodyCallees(s.Type),
			})
		case *ast.ValueSpec:
			out = append(out, extractGoValueDecls(path, content, fset, decl, s, groupDoc)...)
		}
	}
	return out
}

func extractGoValueDecls(path, content string, fset *token.FileSet, decl *ast.GenDecl, spec *ast.ValueSpec, groupDoc string) []goContractDecl {
	start := fset.Position(spec.Pos()).Line
	end := fset.Position(spec.End()).Line
	doc := firstNonEmpty(cleanGoDoc(spec.Doc), groupDoc)
	prefix := decl.Tok.String() + " "
	lineText := strings.TrimSpace(sourceLines(content, start, start))
	if strings.HasPrefix(lineText, decl.Tok.String()+" ") {
		prefix = ""
	}
	sig := cleanGoSignature(prefix + lineText)
	callees := collectGoExprListCallees(spec.Values)
	snippet := formatGoValueSnippet(doc, prefix, content, start, end)
	var out []goContractDecl
	for _, ident := range spec.Names {
		if ident.Name == "_" {
			continue
		}
		out = append(out, goContractDecl{
			name:      ident.Name,
			baseName:  ident.Name,
			kind:      strings.ToLower(decl.Tok.String()),
			exported:  ident.IsExported(),
			file:      path,
			startLine: start,
			endLine:   end,
			doc:       doc,
			signature: sig,
			snippet:   snippet,
			callees:   callees,
		})
	}
	return out
}

func formatGoValueSnippet(doc, prefix, content string, start, end int) string {
	raw := boundSnippetLines(sourceLines(content, start, end), maxContractDeclLines)
	if raw != "" && prefix != "" && !strings.HasPrefix(strings.TrimSpace(raw), strings.TrimSpace(prefix)) {
		raw = prefix + raw
	}
	return prependContractDoc(doc, raw)
}

func formatGoDeclSnippet(content, doc, sig string, start, end int, signatureOnly bool) string {
	if signatureOnly {
		return prependContractDoc(doc, sig)
	}
	raw := boundSnippetLines(sourceLines(content, start, end), maxContractDeclLines)
	if raw == "" {
		raw = sig
	}
	return prependContractDoc(doc, raw)
}

func prependContractDoc(doc, body string) string {
	if doc == "" {
		return body
	}
	return "// " + doc + "\n" + body
}

func boundSnippetLines(raw string, maxLines int) string {
	if raw == "" {
		return ""
	}
	lines := strings.Split(raw, "\n")
	if len(lines) <= maxLines {
		return raw
	}
	return strings.Join(lines[:maxLines], "\n") + "\n// ..."
}

func cleanGoDoc(group *ast.CommentGroup) string {
	if group == nil {
		return ""
	}
	text := strings.Join(strings.Fields(group.Text()), " ")
	return textutil.Clip(text, maxContractDocRunes)
}

func cleanGoSignature(raw string) string {
	if idx := strings.IndexByte(raw, '{'); idx >= 0 {
		raw = raw[:idx]
	}
	return strings.Join(strings.Fields(raw), " ")
}

func goReceiverTypeName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	expr := fn.Recv.List[0].Type
	for {
		switch t := expr.(type) {
		case *ast.StarExpr:
			expr = t.X
		case *ast.IndexExpr:
			expr = t.X
		case *ast.IndexListExpr:
			expr = t.X
		case *ast.ParenExpr:
			expr = t.X
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
}

func collectGoBodyCallees(node ast.Node) []string {
	if node == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	ast.Inspect(node, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			if !seen[x.Name] {
				seen[x.Name] = true
				out = append(out, x.Name)
			}
		case *ast.SelectorExpr:
			if !seen[x.Sel.Name] {
				seen[x.Sel.Name] = true
				out = append(out, x.Sel.Name)
			}
		}
		return true
	})
	return out
}

func collectGoExprListCallees(exprs []ast.Expr) []string {
	seen := map[string]bool{}
	var out []string
	for _, expr := range exprs {
		for _, name := range collectGoBodyCallees(expr) {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}

func collectGoTargetRefs(targetAST *ast.File, importedByRaw map[string][]string, parsedMods map[string]*goModuleInfo) goTargetRefs {
	refs := goTargetRefs{
		selectors:   map[string]map[string]bool{},
		methods:     map[string]bool{},
		freeIdents:  map[string]bool{},
		aliasToMod:  map[string][]string{},
		dotImported: map[string]bool{},
	}
	for _, imp := range targetAST.Imports {
		rawPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		mods := importedByRaw[rawPath]
		if len(mods) == 0 {
			continue
		}
		registerGoImportAlias(imp, mods, parsedMods, &refs)
	}
	skipIdents := map[*ast.Ident]bool{}
	ast.Inspect(targetAST, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.ImportSpec:
			return false
		case *ast.SelectorExpr:
			skipIdents[x.Sel] = true
			recordGoSelectorRef(x, &refs)
		case *ast.KeyValueExpr:
			if key, ok := x.Key.(*ast.Ident); ok {
				skipIdents[key] = true
			}
		case *ast.Ident:
			if !skipIdents[x] && x.Obj == nil && x.Name != "_" {
				refs.freeIdents[x.Name] = true
			}
		}
		return true
	})
	return refs
}

func registerGoImportAlias(imp *ast.ImportSpec, mods []string, parsedMods map[string]*goModuleInfo, refs *goTargetRefs) {
	if imp.Name != nil {
		switch imp.Name.Name {
		case "_":
			return
		case ".":
			for _, mod := range mods {
				refs.dotImported[mod] = true
			}
			return
		default:
			refs.aliasToMod[imp.Name.Name] = append(refs.aliasToMod[imp.Name.Name], mods...)
			return
		}
	}
	for _, mod := range mods {
		alias := filepath.Base(mod)
		if info := parsedMods[mod]; info != nil && info.pkgName != "" {
			alias = info.pkgName
		}
		refs.aliasToMod[alias] = append(refs.aliasToMod[alias], mod)
	}
}

func recordGoSelectorRef(sel *ast.SelectorExpr, refs *goTargetRefs) {
	refs.methods[sel.Sel.Name] = true
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return
	}
	bucket := refs.selectors[ident.Name]
	if bucket == nil {
		bucket = map[string]bool{}
		refs.selectors[ident.Name] = bucket
	}
	bucket[sel.Sel.Name] = true
}

func renderGoModuleSection(mod string, isSameModule bool, info *goModuleInfo, refs goTargetRefs, used map[string]int) string {
	seeds := seedGoModuleDecls(mod, isSameModule, info, refs)
	if len(seeds) == 0 {
		return ""
	}
	selected := expandGoCallees(info, seeds)
	sort.Ints(selected)
	var b strings.Builder
	title := mod
	if info.pkgName != "" {
		title += " (" + info.pkgName + ")"
	}
	b.WriteString("### Package " + title + "\n")
	b.WriteString("Referenced contracts and call/state chain:\n")
	writeSelectedGoDecls(&b, info, selected, used)
	if !isSameModule {
		writePeerExportedSignatures(&b, mod, info, selected, used)
	}
	return strings.TrimSpace(b.String())
}

func seedGoModuleDecls(mod string, isSameModule bool, info *goModuleInfo, refs goTargetRefs) []int {
	seen := map[int]bool{}
	var seeds []int
	addAll := func(indices []int, exportedOnly bool) {
		for _, idx := range indices {
			if !seen[idx] && (!exportedOnly || info.decls[idx].exported) {
				seen[idx] = true
				seeds = append(seeds, idx)
			}
		}
	}
	if isSameModule || refs.dotImported[mod] {
		exportedOnly := !isSameModule
		for ident := range refs.freeIdents {
			addAll(info.byName[ident], exportedOnly)
			addAll(info.byBase[ident], exportedOnly)
		}
		for meth := range refs.methods {
			addAll(info.byMeth[meth], exportedOnly)
		}
		return seeds
	}
	for alias, mods := range refs.aliasToMod {
		for _, targetMod := range mods {
			if targetMod != mod {
				continue
			}
			for sym := range refs.selectors[alias] {
				addAll(info.byName[sym], true)
			}
		}
	}
	seedReferencedTypeMethods(info, refs.methods, &seen, &seeds)
	return seeds
}

func seedReferencedTypeMethods(info *goModuleInfo, methodRefs map[string]bool, seen *map[int]bool, seeds *[]int) {
	referencedTypes := map[string]bool{}
	for _, idx := range *seeds {
		decl := info.decls[idx]
		if decl.kind == "type" {
			referencedTypes[decl.name] = true
		}
		for _, callee := range decl.callees {
			for _, typeIdx := range info.byName[callee] {
				if info.decls[typeIdx].kind == "type" {
					referencedTypes[info.decls[typeIdx].name] = true
				}
			}
		}
	}
	if len(referencedTypes) == 0 {
		return
	}
	for meth := range methodRefs {
		for _, idx := range info.byMeth[meth] {
			decl := info.decls[idx]
			recv := strings.TrimSuffix(decl.name, "."+decl.methodName)
			if decl.exported && referencedTypes[recv] && !(*seen)[idx] {
				(*seen)[idx] = true
				*seeds = append(*seeds, idx)
			}
		}
	}
}

func expandGoCallees(info *goModuleInfo, seeds []int) []int {
	selectedMap := map[int]bool{}
	queue := make([]struct {
		idx, depth int
	}, 0, len(seeds))
	for _, idx := range seeds {
		selectedMap[idx] = true
		queue = append(queue, struct{ idx, depth int }{idx: idx, depth: 0})
	}
	helpersAdded := 0
	for len(queue) > 0 && helpersAdded < maxContractHelpers {
		item := queue[0]
		queue = queue[1:]
		if item.depth >= maxContractHelperDepth {
			continue
		}
		helpersAdded = enqueueCalleeMatches(info, item.idx, item.depth, selectedMap, &queue, helpersAdded)
	}
	selected := make([]int, 0, len(selectedMap))
	for idx := range selectedMap {
		selected = append(selected, idx)
	}
	return selected
}

func enqueueCalleeMatches(info *goModuleInfo, idx, depth int, selected map[int]bool, queue *[]struct{ idx, depth int }, helpersAdded int) int {
	for _, callee := range info.decls[idx].callees {
		for _, nextIdx := range matchGoDeclIndices(info, callee) {
			if selected[nextIdx] {
				continue
			}
			selected[nextIdx] = true
			helpersAdded++
			*queue = append(*queue, struct{ idx, depth int }{idx: nextIdx, depth: depth + 1})
			if helpersAdded >= maxContractHelpers {
				return helpersAdded
			}
		}
	}
	return helpersAdded
}

func matchGoDeclIndices(info *goModuleInfo, symbol string) []int {
	var out []int
	out = append(out, info.byName[symbol]...)
	out = append(out, info.byMeth[symbol]...)
	return out
}

func writeSelectedGoDecls(b *strings.Builder, info *goModuleInfo, selected []int, used map[string]int) {
	for _, idx := range selected {
		decl := info.decls[idx]
		entry := fmt.Sprintf("- %s:%d:\n%s\n", decl.file, decl.startLine, indentBlock(decl.snippet, "  "))
		b.WriteString(entry)
		used[decl.file] += len(entry)
	}
}

func writePeerExportedSignatures(b *strings.Builder, mod string, info *goModuleInfo, selected []int, used map[string]int) {
	selectedSet := map[int]bool{}
	selectedSymbols := map[string]bool{}
	for _, idx := range selected {
		selectedSet[idx] = true
		selectedSymbols[info.decls[idx].name] = true
		selectedSymbols[info.decls[idx].baseName] = true
	}
	peers := rankedPeerExportedDecls(info, selectedSet, selectedSymbols)
	if len(peers) == 0 {
		return
	}
	if len(peers) > maxPackagePeerSignatures {
		peers = peers[:maxPackagePeerSignatures]
	}
	b.WriteString("Related exported signatures in " + mod + ":\n")
	for _, idx := range peers {
		decl := info.decls[idx]
		line := fmt.Sprintf("- %s (%s:%d)", decl.signature, decl.file, decl.startLine)
		if decl.doc != "" {
			line += " // " + firstSentence(decl.doc)
		}
		line += "\n"
		b.WriteString(line)
		used[decl.file] += len(line)
	}
}

func rankedPeerExportedDecls(info *goModuleInfo, selectedSet map[int]bool, selectedSymbols map[string]bool) []int {
	type scored struct {
		idx     int
		overlap int
	}
	var peers []scored
	for idx, decl := range info.decls {
		if selectedSet[idx] || !decl.exported || (decl.kind != "function" && decl.kind != "method") {
			continue
		}
		overlap := 0
		for _, callee := range decl.callees {
			if selectedSymbols[callee] {
				overlap++
			}
		}
		peers = append(peers, scored{idx: idx, overlap: overlap})
	}
	sort.SliceStable(peers, func(i, j int) bool {
		if peers[i].overlap != peers[j].overlap {
			return peers[i].overlap > peers[j].overlap
		}
		return peers[i].idx < peers[j].idx
	})
	out := make([]int, 0, len(peers))
	for _, p := range peers {
		out = append(out, p.idx)
	}
	return out
}

// --- Non-Go contract extraction ---

var identTokenPattern = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

func buildGenericContracts(targetPath, targetSource, sameModule string, byModule map[string][]loadedContractFile, modules []string) contractEmission {
	emission := contractEmission{used: map[string]int{}}
	targetTokens := tokenizeIdents(targetSource)
	localSymbols := localDeclaredBaseNames(targetPath, targetSource)
	for _, mod := range modules {
		section := buildGenericModuleSection(mod, mod == sameModule, byModule[mod], targetTokens, localSymbols, emission.used)
		if section != "" {
			emission.sections = append(emission.sections, section)
		}
	}
	return emission
}

func buildGenericModuleSection(mod string, isSameModule bool, files []loadedContractFile, targetTokens, localSymbols map[string]bool, used map[string]int) string {
	var b strings.Builder
	count := 0
	for _, f := range files {
		count += appendGenericFileDecls(&b, f, isSameModule, targetTokens, localSymbols, used, maxContractHelpers-count)
		if count >= maxContractHelpers {
			break
		}
	}
	if count == 0 {
		return ""
	}
	return "### Module " + mod + "\nReferenced contracts:\n" + strings.TrimSpace(b.String())
}

func appendGenericFileDecls(b *strings.Builder, f loadedContractFile, isSameModule bool, targetTokens, localSymbols map[string]bool, used map[string]int, remaining int) int {
	if remaining <= 0 {
		return 0
	}
	outline := syntax.Outline(f.file.Path, []byte(f.content))
	if len(outline) == 0 {
		return 0
	}
	spans := syntaxFunctionSpans(f.file.Path, []byte(f.content))
	emitted := 0
	for _, sym := range outline {
		base := sym.Name
		if dot := strings.LastIndexByte(base, '.'); dot >= 0 {
			base = base[dot+1:]
		}
		if !targetTokens[base] || (isSameModule && localSymbols[base]) {
			continue
		}
		snippet := genericSymbolSnippet(f.content, sym, spans[sym.Name])
		entry := fmt.Sprintf("- %s:%d:\n%s\n", f.file.Path, sym.Line, indentBlock(snippet, "  "))
		b.WriteString(entry)
		used[f.file.Path] += len(entry)
		emitted++
		if emitted >= remaining {
			break
		}
	}
	return emitted
}

func syntaxFunctionSpans(path string, content []byte) map[string]syntax.Function {
	analyzed := syntax.Analyze(path, content)
	spans := make(map[string]syntax.Function, len(analyzed.Functions))
	for _, fn := range analyzed.Functions {
		spans[fn.Name] = fn
	}
	return spans
}

func genericSymbolSnippet(content string, sym syntax.Symbol, fn syntax.Function) string {
	comment := leadingCommentLines(content, sym.Line, 3)
	body := sym.Signature
	if fn.EndLine >= fn.StartLine && fn.StartLine > 0 {
		body = boundSnippetLines(sourceLines(content, fn.StartLine, fn.EndLine), maxContractDeclLines)
	}
	if comment == "" {
		return body
	}
	return comment + "\n" + body
}

func leadingCommentLines(content string, line, maxLines int) string {
	if line <= 1 {
		return ""
	}
	lines := strings.Split(content, "\n")
	start := max(0, line-1-maxLines)
	var comments []string
	for idx := line - 2; idx >= start; idx-- {
		trimmed := strings.TrimSpace(lines[idx])
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") {
			comments = append(comments, trimmed)
		} else {
			break
		}
	}
	for i, j := 0, len(comments)-1; i < j; i, j = i+1, j-1 {
		comments[i], comments[j] = comments[j], comments[i]
	}
	return strings.Join(comments, "\n")
}

func tokenizeIdents(text string) map[string]bool {
	matches := identTokenPattern.FindAllString(text, -1)
	tokens := make(map[string]bool, len(matches))
	for _, m := range matches {
		tokens[m] = true
	}
	return tokens
}

func localDeclaredBaseNames(path, source string) map[string]bool {
	names := map[string]bool{}
	for _, sym := range syntax.Outline(path, []byte(source)) {
		base := sym.Name
		if dot := strings.LastIndexByte(base, '.'); dot >= 0 {
			base = base[dot+1:]
		}
		names[base] = true
	}
	return names
}

func indentBlock(text, prefix string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func firstSentence(text string) string {
	if idx := strings.Index(text, ". "); idx >= 0 {
		return text[:idx+1]
	}
	return textutil.Clip(text, 120)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
