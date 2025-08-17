package precompiler

import (
	"fmt"
	"strings"

	"github.com/pkg/errors"
	"github.com/ysugimoto/falco/ast"
	"github.com/ysugimoto/falco/interpreter/context"
	"github.com/ysugimoto/falco/interpreter/exception"
	"github.com/ysugimoto/falco/lexer"
	"github.com/ysugimoto/falco/parser"
	"github.com/ysugimoto/falco/resolver"
	"github.com/ysugimoto/falco/snippet"
)

type PrecompiledVCL struct {
	MainStatements []ast.Statement
	Subroutines    map[string]*PrecompiledSubroutine
}

func (p *PrecompiledVCL) GetMainStatements() []ast.Statement {
	return p.MainStatements
}

func (p *PrecompiledVCL) GetSubroutines() map[string]context.PrecompiledSubroutine {
	result := make(map[string]context.PrecompiledSubroutine)
	for name, sub := range p.Subroutines {
		result[name] = sub
	}
	return result
}

type PrecompiledSubroutine struct {
	Declaration        *ast.SubroutineDeclaration
	ResolvedStatements []ast.Statement
}

func (p *PrecompiledSubroutine) GetDeclaration() *ast.SubroutineDeclaration {
	return p.Declaration
}

func (p *PrecompiledSubroutine) GetResolvedStatements() []ast.Statement {
	return p.ResolvedStatements
}

type Precompiler struct {
	resolver resolver.Resolver
	snippets *snippet.Snippets
}

func New(rslv resolver.Resolver, snippets *snippet.Snippets) *Precompiler {
	return &Precompiler{
		resolver: rslv,
		snippets: snippets,
	}
}

func (p *Precompiler) Precompile(enableTLS bool) (*PrecompiledVCL, error) {
	main, err := p.resolver.MainVCL()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	vcl, err := parser.New(
		lexer.NewFromString(main.Data, lexer.WithFile(main.Name)),
	).ParseVCL()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	// Embed remote snippets if they exist
	if p.snippets != nil {
		snippets, err := p.snippets.EmbedSnippets(enableTLS)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		for _, snip := range snippets {
			s, err := parser.New(lexer.NewFromString(snip.Data, lexer.WithFile(snip.Name))).ParseVCL()
			if err != nil {
				return nil, errors.WithStack(err)
			}
			vcl.Statements = append(s.Statements, vcl.Statements...)
		}
	}

	// Resolve all include statements
	resolvedStatements, err := p.resolveIncludeStatements(vcl.Statements, true)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	// Extract subroutines and precompile them
	subroutines := make(map[string]*PrecompiledSubroutine)
	var mainStatements []ast.Statement

	for _, stmt := range resolvedStatements {
		if sub, ok := stmt.(*ast.SubroutineDeclaration); ok {
			precompiledSub, err := p.precompileSubroutine(sub)
			if err != nil {
				return nil, errors.WithStack(err)
			}
			subroutines[sub.Name.Value] = precompiledSub
		} else {
			mainStatements = append(mainStatements, stmt)
		}
	}

	return &PrecompiledVCL{
		MainStatements: mainStatements,
		Subroutines:    subroutines,
	}, nil
}

func (p *Precompiler) precompileSubroutine(sub *ast.SubroutineDeclaration) (*PrecompiledSubroutine, error) {
	// Resolve include statements within subroutine
	resolvedStatements, err := p.resolveIncludeStatements(sub.Block.Statements, false)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	// Extract boilerplate macros
	statements, err := p.extractBoilerplateMacro(sub, resolvedStatements)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return &PrecompiledSubroutine{
		Declaration:        sub,
		ResolvedStatements: statements,
	}, nil
}

func (p *Precompiler) resolveIncludeStatements(statements []ast.Statement, isRoot bool) ([]ast.Statement, error) {
	var resolved []ast.Statement
	for _, stmt := range statements {
		if include, ok := stmt.(*ast.IncludeStatement); ok {
			if strings.HasPrefix(include.Module.Value, "snippet::") {
				if included, err := p.includeSnippet(include, isRoot); err != nil {
					return nil, exception.Runtime(&stmt.GetMeta().Token, "%s", err.Error())
				} else {
					resolved = append(resolved, included...)
				}
				continue
			}
			included, err := p.includeFile(include, isRoot)
			if err != nil {
				return nil, exception.Runtime(&stmt.GetMeta().Token, "%s", err.Error())
			}
			recursive, err := p.resolveIncludeStatements(included, isRoot)
			if err != nil {
				return nil, err
			}
			resolved = append(resolved, recursive...)
			continue
		}
		resolved = append(resolved, stmt)
	}

	return resolved, nil
}

func (p *Precompiler) includeSnippet(include *ast.IncludeStatement, isRoot bool) ([]ast.Statement, error) {
	if p.snippets == nil {
		return nil, exception.Runtime(
			&include.GetMeta().Token, "Remote snippet is not found. Did you run with '-r' option?",
		)
	}
	snippets := p.snippets.IncludeSnippets
	snip, ok := snippets[strings.TrimPrefix(include.Module.Value, "snippet::")]
	if !ok {
		return nil, fmt.Errorf("Failed to include VCL snippets '%s'", include.Module.Value)
	}
	if isRoot {
		return p.loadRootVCL(include.Module.Value, snip.Data)
	}
	return p.loadStatementVCL(include.Module.Value, snip.Data)
}

func (p *Precompiler) includeFile(include *ast.IncludeStatement, isRoot bool) ([]ast.Statement, error) {
	module, err := p.resolver.Resolve(include)
	if err != nil {
		return nil, fmt.Errorf("Failed to include VCL module '%s'", include.Module.Value)
	}

	if isRoot {
		return p.loadRootVCL(module.Name, module.Data)
	}
	return p.loadStatementVCL(module.Name, module.Data)
}

func (p *Precompiler) loadRootVCL(name, content string) ([]ast.Statement, error) {
	lx := lexer.NewFromString(content, lexer.WithFile(name))
	vcl, err := parser.New(lx).ParseVCL()
	if err != nil {
		return nil, err
	}
	return vcl.Statements, nil
}

func (p *Precompiler) loadStatementVCL(name, content string) ([]ast.Statement, error) {
	lx := lexer.NewFromString(content, lexer.WithFile(name))
	vcl, err := parser.New(lx).ParseSnippetVCL()
	if err != nil {
		return nil, err
	}
	return vcl, nil
}

func (p *Precompiler) extractBoilerplateMacro(sub *ast.SubroutineDeclaration, statements []ast.Statement) ([]ast.Statement, error) {
	if p.snippets == nil {
		return statements, nil
	}

	// If subroutine name is fastly subroutine, find and extract boilerplate macro
	macro, ok := context.FastlyReservedSubroutine[sub.Name.Value]
	if !ok {
		return statements, nil
	}
	snippets, ok := p.snippets.ScopedSnippets[macro]
	if !ok || len(snippets) == 0 {
		return statements, nil
	}

	macroName := strings.ToUpper("fastly " + macro)

	var resolved []ast.Statement
	// Find "FASTLY [macro]" comment and extract in infix comment of block statement
	if p.hasFastlyBoilerplateMacro(sub.Block.Infix, macroName) {
		for _, s := range snippets {
			stmts, err := p.loadStatementVCL(s.Name, s.Data)
			if err != nil {
				return nil, errors.WithStack(err)
			}
			resolved = append(resolved, stmts...)
		}
		// Prepend to block statements
		resolved = append(resolved, statements...)
		return resolved, nil
	}

	// Find "FASTLY [macro]" comment and extract inside block statement
	var found bool // guard flag, embedding macro should do only once
	for _, stmt := range statements {
		if p.hasFastlyBoilerplateMacro(stmt.GetMeta().Leading, macroName) && !found {
			for _, s := range snippets {
				stmts, err := p.loadStatementVCL(s.Name, s.Data)
				if err != nil {
					return nil, errors.WithStack(err)
				}
				resolved = append(resolved, stmts...)
			}
			found = true // guard for only once
		}
		resolved = append(resolved, stmt) // don't forget to append original statement
	}
	return resolved, nil
}

func (p *Precompiler) hasFastlyBoilerplateMacro(cs ast.Comments, macroName string) bool {
	for _, c := range cs {
		line := strings.TrimLeft(c.String(), " */#")
		if strings.HasPrefix(strings.ToUpper(line), macroName) {
			return true
		}
	}
	return false
}
