package securityrules

import (
	"go/ast"
)

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

func bindRangeName(scope *securityRuleScope, expression ast.Expr) {
	if name, ok := expression.(*ast.Ident); ok {
		scope.bind(name.Name)
	}
}
