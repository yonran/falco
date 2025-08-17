package tester

import (
	"fmt"
	ghttp "net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
	"github.com/ysugimoto/falco/ast"
	"github.com/ysugimoto/falco/config"
	"github.com/ysugimoto/falco/interpreter"
	"github.com/ysugimoto/falco/interpreter/context"
	"github.com/ysugimoto/falco/interpreter/function"
	"github.com/ysugimoto/falco/interpreter/http"
	"github.com/ysugimoto/falco/interpreter/precompiler"
	"github.com/ysugimoto/falco/interpreter/value"
	"github.com/ysugimoto/falco/interpreter/variable"
	"github.com/ysugimoto/falco/lexer"
	"github.com/ysugimoto/falco/parser"
	"github.com/ysugimoto/falco/resolver"
	tf "github.com/ysugimoto/falco/tester/function"
	"github.com/ysugimoto/falco/tester/shared"
	"github.com/ysugimoto/falco/tester/syntax"
	tv "github.com/ysugimoto/falco/tester/variable"
)

var (
	defaultTimeout = 10 // testing process will be timeouted in 10 minutes
	ErrTimeout     = errors.New("Timeout")
)

type Tester struct {
	interpreterOptions []context.Option
	config             *config.TestConfig
	counter            *shared.Counter
	coverage           *shared.Coverage
}

func New(c *config.TestConfig, opts []context.Option) *Tester {
	t := &Tester{
		interpreterOptions: opts,
		config:             c,
		counter:            shared.NewCounter(),
	}
	if c.Coverage {
		t.coverage = shared.NewCoverage()
		t.interpreterOptions = append(t.interpreterOptions, context.WithCoverage(t.coverage))
	}
	return t
}

// Find test target VCL files
// Note that:
// - Test files must have ".test.vcl" extension e.g default.test.vcl
// - Tester finds files from all include paths
func (t *Tester) listTestFiles(main string) ([]string, error) {
	// correct include paths
	searchDirs := []string{filepath.Dir(main)}
	searchDirs = append(searchDirs, t.config.IncludePaths...)

	var testFiles []string
	for i := range searchDirs {
		files, err := findTestTargetFiles(searchDirs[i], t.config.Filter)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		testFiles = append(testFiles, files...)
	}

	return dedupeFiles(testFiles), nil
}

// Only expose function for running tests
func (t *Tester) Run(main string) (*TestFactory, error) {
	// Find test target VCL files
	targetFiles, err := t.listTestFiles(main)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	// Run tests - each test file gets its own independent precompiled VCL
	var results []*TestResult
	for i := range targetFiles {
		result, err := t.run(targetFiles[i])
		if err != nil {
			return nil, errors.WithStack(err)
		}
		results = append(results, result)
	}

	factory := &TestFactory{
		Results:    results,
		Statistics: t.counter,
	}
	if t.coverage != nil {
		factory.Coverage = t.coverage.Factory()
	}
	return factory, nil
}

// Create precompiled VCL for a specific test file
func (t *Tester) createPrecompiledVCLWithTests(testFile string) (context.PrecompiledVCL, error) {
	// Extract resolver and snippets from interpreter options
	// Initialize context with required maps to avoid nil pointer panics
	ctx := &context.Context{
		OverrideVariables: make(map[string]value.Value),
	}
	for _, opt := range t.interpreterOptions {
		opt(ctx)
	}

	if ctx.Resolver == nil {
		return nil, errors.New("No resolver found in interpreter options")
	}

	// Create standard precompiler first
	pc := precompiler.New(ctx.Resolver, ctx.FastlySnippets)
	standardVCL, err := pc.Precompile(false)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	// Parse the specific test file and extract its subroutines
	testSubroutines := make(map[string]*ast.SubroutineDeclaration)
	content, err := os.ReadFile(testFile)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	l := lexer.NewFromString(string(content), lexer.WithFile(testFile))
	vcl, err := parser.New(l, parser.WithCustomParser(syntax.CustomParsers()...)).ParseVCL()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	// Extract subroutines from both direct declarations and describe blocks
	// For describe blocks, namespace subroutines to avoid name conflicts
	// For global subroutines with duplicate names, create unique keys using index
	globalSubNames := make(map[string]int)              // Track count of each subroutine name
	globalSubroutines := []*ast.SubroutineDeclaration{} // Store subroutines in order

	for _, stmt := range vcl.Statements {
		switch st := stmt.(type) {
		case *ast.SubroutineDeclaration:
			globalSubroutines = append(globalSubroutines, st)
			globalSubNames[st.Name.Value]++
		case *syntax.DescribeStatement:
			// Namespace subroutines by describe block name to avoid conflicts
			describePrefix := st.Name.String()
			for _, sub := range st.Subroutines {
				namespacedName := describePrefix + "." + sub.Name.Value
				testSubroutines[namespacedName] = sub
			}
		}
	}

	// Now register global subroutines with unique keys only for duplicates
	nameCounters := make(map[string]int)
	for _, sub := range globalSubroutines {
		if globalSubNames[sub.Name.Value] > 1 {
			// Multiple subroutines with same name - use unique key
			uniqueKey := fmt.Sprintf("%s#%d", sub.Name.Value, nameCounters[sub.Name.Value])
			testSubroutines[uniqueKey] = sub
			nameCounters[sub.Name.Value]++
		} else {
			// Single subroutine with this name - use original name
			testSubroutines[sub.Name.Value] = sub
		}
	}

	// Create combined precompiled VCL
	combinedSubroutines := make(map[string]*precompiler.PrecompiledSubroutine)

	// Add standard subroutines (check for nil map)
	if standardVCL.Subroutines != nil {
		for name, sub := range standardVCL.Subroutines {
			combinedSubroutines[name] = sub
		}
	}

	// Add test subroutines (precompile them through the same machinery as standard VCL)
	for name, sub := range testSubroutines {
		// Create a properly precompiled subroutine using the same precompiler
		// Since precompileSubroutine is private, we need to do minimal precompilation here
		// Test subroutines typically don't have includes or Fastly macros, so this should be sufficient
		precompiledSub := &precompiler.PrecompiledSubroutine{
			Declaration:        sub,
			ResolvedStatements: sub.Block.Statements,
		}
		combinedSubroutines[name] = precompiledSub
	}

	return &precompiler.PrecompiledVCL{
		MainStatements: standardVCL.MainStatements,
		Subroutines:    combinedSubroutines,
	}, nil
}

// Actually run testing method
func (t *Tester) run(testFile string) (*TestResult, error) {
	// Create precompiled VCL specific to this test file
	precompiledVCL, err := t.createPrecompiledVCLWithTests(testFile)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	// Create interpreter options with this test file's precompiled VCL
	testOptions := append(t.interpreterOptions, context.WithPrecompiledVCL(precompiledVCL))

	resolvers, err := resolver.NewFileResolvers(testFile, t.config.IncludePaths)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	main, err := resolvers[0].MainVCL()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	l := lexer.NewFromString(main.Data, lexer.WithFile(main.Name))
	vcl, err := parser.New(l, parser.WithCustomParser(syntax.CustomParsers()...)).ParseVCL()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	errChan := make(chan error)
	finishChan := make(chan []*TestCase)

	timeout := defaultTimeout
	if t.config.Timeout > 0 {
		timeout = t.config.Timeout
	}
	timeoutChan := time.After(time.Duration(timeout) * time.Minute)

	go func(vcl *ast.VCL) {
		// Factory definitions in the test file
		defs := t.factoryDefinitions(vcl)
		var cases []*TestCase

		// First pass: count subroutine names to identify duplicates
		globalSubNames := make(map[string]int)
		for _, stmt := range vcl.Statements {
			if st, ok := stmt.(*ast.SubroutineDeclaration); ok {
				globalSubNames[st.Name.Value]++
			}
		}

		// Second pass: process subroutines with appropriate keys
		nameCounters := make(map[string]int)
		for _, stmt := range vcl.Statements {
			switch st := stmt.(type) {
			case *syntax.DescribeStatement:
				results, err := t.runDescribedTests(testOptions, defs, st)
				if len(results) > 0 {
					cases = append(cases, results...)
				}
				if err != nil {
					errChan <- errors.WithStack(err)
					return
				}
			case *ast.SubroutineDeclaration:
				// Some functions like "testing.table_set()" will take side-effect for another testing subroutine
				// so we always initialize interpreter, inject testing functions for each subroutine
				i := t.setupInterpreter(testOptions, defs)

				mockRequest, err := http.NewRequest(ghttp.MethodGet, "http://localhost", nil)
				if err != nil {
					errChan <- errors.WithStack(err)
					return
				}
				if err := i.TestProcessInit(mockRequest); err != nil {
					errChan <- errors.WithStack(err)
					return
				}

				// Determine the key for this subroutine (unique key only for duplicates)
				var subroutineKey string
				if globalSubNames[st.Name.Value] > 1 {
					// Multiple subroutines with same name - use unique key
					subroutineKey = fmt.Sprintf("%s#%d", st.Name.Value, nameCounters[st.Name.Value])
					nameCounters[st.Name.Value]++
				} else {
					// Single subroutine with this name - use original name
					subroutineKey = st.Name.Value
				}

				metadata := getTestMetadata(st)
				for _, s := range metadata.Scopes {
					// Attach new debugger for each test suite
					d := NewDebugger()
					i.Debugger = d

					// Skip this testsuite when marked as @skip or @tag matched
					if metadata.Skip || metadata.MatchTags(t.config.Tags) {
						cases = append(cases, &TestCase{
							Name:  metadata.Name,
							Scope: s.String(),
							Skip:  true,
						})
						t.counter.Skip()
						continue
					}

					// Set the key only for this specific test execution
					i.SetCurrentSubroutineKey(subroutineKey)
					start := time.Now()
					err := i.ProcessTestSubroutine(s, st)
					// Clear the key after test execution to avoid affecting other subroutine calls
					i.SetCurrentSubroutineKey("")

					cases = append(cases, &TestCase{
						Name:  metadata.Name,
						Error: errors.Cause(err),
						Scope: s.String(),
						Time:  time.Since(start).Milliseconds(),
						Logs:  d.stack,
					})
					if err != nil {
						t.counter.Fail()
					}
				}
			}
		}

		finishChan <- cases
	}(vcl)

	// Aggregate asynchronous channels
	select {
	case err := <-errChan:
		return nil, err
	case <-timeoutChan:
		return nil, ErrTimeout
	case cases := <-finishChan:
		return &TestResult{
			Filename: testFile,
			Cases:    cases,
			Lexer:    l,
		}, nil
	}
}

func (t *Tester) runDescribedTests(
	interpreterOptions []context.Option,
	defs *tf.Definiions,
	d *syntax.DescribeStatement,
) ([]*TestCase, error) {

	var cases []*TestCase
	mockRequest, err := http.NewRequest(ghttp.MethodGet, "http://localhost", nil)
	if err != nil {
		return cases, errors.WithStack(err)
	}

	// describe should run as group testing, create interpreter once through tests
	i := t.setupInterpreter(interpreterOptions, defs)

	if err := i.TestProcessInit(mockRequest); err != nil {
		return cases, errors.WithStack(err)
	}

	// Set the current describe scope for namespaced subroutine lookup (after initialization)
	i.SetCurrentDescribeScope(d.Name.String())

	defer func() {
		// Clear the describe scope and remove stored subroutines
		i.SetCurrentDescribeScope("")
		for _, sub := range d.Subroutines {
			delete(defs.Subroutines, sub.Name.Value)
		}
	}()

	// Prepare to add subroutine definitions inside describe statement
	for _, sub := range d.Subroutines {
		defs.Subroutines[sub.Name.Value] = sub
	}

	for _, sub := range d.Subroutines {
		metadata := getTestMetadata(sub)
		for _, s := range metadata.Scopes {
			// Attach new debugger for each test suite
			debugger := NewDebugger()
			i.Debugger = debugger

			// Skip this testsuite when marked as @skip or @tag matched
			if metadata.Skip || metadata.MatchTags(t.config.Tags) {
				cases = append(cases, &TestCase{
					Name:  metadata.Name,
					Scope: s.String(),
					Skip:  true,
				})
				t.counter.Skip()
				continue
			}

			// Run before_xxx hook that corresponds to scope is exists
			if hook, ok := d.Befores[strings.ToLower("before_"+s.String())]; ok {
				i.SetScope(s)
				if _, _, _, err := i.ProcessBlockStatement(
					hook.Block.Statements,
					interpreter.DebugPass,
					false,
				); err != nil {
					return cases, err
				}
			}

			start := time.Now()
			err := i.ProcessTestSubroutine(s, sub)
			cases = append(cases, &TestCase{
				Name:  metadata.Name,
				Group: d.Name.String(),
				Error: errors.Cause(err),
				Scope: s.String(),
				Time:  time.Since(start).Milliseconds(),
				Logs:  debugger.stack,
			})
			if err != nil {
				t.counter.Fail()
			}

			// Run after_xxx hook that corresponds to scope is exists
			if hook, ok := d.Afters[strings.ToLower("after_"+s.String())]; ok {
				i.SetScope(s)
				if _, _, _, err := i.ProcessBlockStatement(
					hook.Block.Statements,
					interpreter.DebugPass,
					false,
				); err != nil {
					return cases, err
				}
			}
		}
	}

	return cases, nil
}

// Set up interprete for each test subroutines
func (t *Tester) setupInterpreter(interpreterOptions []context.Option, defs *tf.Definiions) *interpreter.Interpreter {
	i := interpreter.New(interpreterOptions...)
	i.Debugger = NewDebugger() // store the default debugger
	i.IdentResolver = func(val string) value.Value {
		if v, ok := defs.Backends[val]; ok {
			return v
		} else if v, ok := defs.Acls[val]; ok {
			return v
		} else if _, ok := defs.Tables[val]; ok {
			return &value.Ident{Value: val, Literal: true}
		} else if s := interpreter.StateFromString(val); s != interpreter.NONE {
			// Some assertion uses interpreter state so we need to resolve state ident like "lookup" in testing VCL
			return &value.Ident{Value: s.String()}
		}
		return nil
	}
	variable.Inject(&tv.TestingVariables{})
	function.Inject(tf.TestingFunctions(i, defs, t.counter, t.coverage))

	return i
}

// Factory declarations in testing VCL
func (t *Tester) factoryDefinitions(vcl *ast.VCL) *tf.Definiions {
	defs := &tf.Definiions{
		Tables:      make(map[string]*ast.TableDeclaration),
		Backends:    make(map[string]*value.Backend),
		Acls:        make(map[string]*value.Acl),
		Subroutines: make(map[string]*ast.SubroutineDeclaration),
	}

	for _, stmt := range vcl.Statements {
		switch t := stmt.(type) {
		case *ast.TableDeclaration:
			defs.Tables[t.Name.Value] = t
		case *ast.BackendDeclaration:
			v := &atomic.Bool{}
			v.Store(true)
			defs.Backends[t.Name.Value] = &value.Backend{
				Value:   t,
				Healthy: v,
			}
		case *ast.AclDeclaration:
			defs.Acls[t.Name.Value] = &value.Acl{
				Value: t,
			}
		case *ast.SubroutineDeclaration:
			defs.Subroutines[t.Name.Value] = t
		}
	}
	return defs
}
