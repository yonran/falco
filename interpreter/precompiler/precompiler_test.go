package precompiler

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/ysugimoto/falco/ast"
	"github.com/ysugimoto/falco/config"
	"github.com/ysugimoto/falco/formatter"
	"github.com/ysugimoto/falco/resolver"
	"github.com/ysugimoto/falco/snippet"
)

func TestSnippetWithTrailingComment(t *testing.T) {
	// Create a snippet that ends with a comment (like backend selection)
	snippetContent := `set req.backend = F_test;
if (req.url ~ "^/test/") {
  # Condition: test (priority: 10)
  set req.backend = F_other;
} # same-line trailing comment at if
# END test snippet`

	// Create a simple VCL with FASTLY recv macro
	vclContent := `sub vcl_recv {
#FASTLY recv
}`
	expect := `sub vcl_recv {
set req.backend = F_test;
if (req.url ~ "^/test/") {
  # Condition: test (priority: 10)
  set req.backend = F_other;
} # same-line trailing comment at if
# END test snippet
#FASTLY recv
}
`

	// Create snippets with the test content
	snippets := &snippet.Snippets{
		ScopedSnippets: snippet.ScopedSnippets{
			"recv": []snippet.Item{{
				Name: "TestSnippet",
				Data: snippetContent,
			}},
		},
	}

	// Create a mock resolver that returns our test VCL
	resolver := &mockResolver{vclContent: vclContent}

	// Create precompiler and precompile
	p := New(resolver, snippets)
	result, err := p.Precompile(false)
	if err != nil {
		t.Fatal(err)
	}

	// Format the result
	f := formatter.New(&config.FormatConfig{
		IndentWidth:          2,
		IndentStyle:          "space",
		TrailingCommentWidth: 1,
	})

	// Get the vcl_recv subroutine
	recvSub, exists := result.GetSubroutines()["vcl_recv"]
	if !exists {
		t.Fatal("vcl_recv subroutine not found")
	}

	// Create the resolved subroutine declaration
	decl := recvSub.GetDeclaration()
	resolvedDecl := &ast.SubroutineDeclaration{
		Meta:       decl.Meta,
		Name:       decl.Name,
		ReturnType: decl.ReturnType,
		Block: &ast.BlockStatement{
			Meta:       decl.Block.Meta,
			Statements: recvSub.GetResolvedStatements(),
		},
	}

	// Format the subroutine
	formatted := f.Format(&ast.VCL{Statements: []ast.Statement{resolvedDecl}})

	// Read the formatted output
	output := make([]byte, 2048)
	n, _ := formatted.Read(output)
	formattedResult := string(output[:n])

	// Count occurrences of the comment
	commentCount := strings.Count(formattedResult, "# END test snippet")

	if diff := cmp.Diff(expect, formattedResult); diff != "" {
		t.Errorf("Format result has diff: %s", diff)
	}

	t.Logf("Formatted output:\n%s", formattedResult)
	t.Logf("Comment '# END test snippet' appears %d times", commentCount)

	if commentCount != 1 {
		t.Errorf("Expected comment to appear exactly 1 time, but got %d times", commentCount)
	}
}

// Mock resolver for testing
type mockResolver struct {
	vclContent string
}

func (m *mockResolver) MainVCL() (*resolver.VCL, error) {
	return &resolver.VCL{
		Name: "test.vcl",
		Data: m.vclContent,
	}, nil
}

func (m *mockResolver) Resolve(include *ast.IncludeStatement) (*resolver.VCL, error) {
	return nil, nil
}

func (m *mockResolver) IncludePaths() []string {
	return []string{}
}

func (m *mockResolver) Name() string {
	return "test"
}
