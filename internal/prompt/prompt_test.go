package prompt

import (
	"strings"
	"testing"

	"github.com/agenticlab-ai/humansh/internal/llm"
)

func TestBuildUsesTheOperatingSystemBaselineWithoutACommandInventory(t *testing.T) {
	t.Parallel()
	request := llm.TranslationRequest{
		Input:          "count the number of lines in the api response of https://fakestoreapi.com/products/1",
		Shell:          "zsh",
		OS:             "darwin",
		Architecture:   "arm64",
		WorkingContext: "humansh",
	}
	prompt, err := Build(request)
	if err != nil {
		t.Fatal(err)
	}
	want := Instruction + "\n\nREQUEST_JSON_BEGIN\n" +
		`{"input":"count the number of lines in the api response of https://fakestoreapi.com/products/1","shell":"zsh","os":"darwin","architecture":"arm64","working_context":"humansh"}` +
		"\nREQUEST_JSON_END\n"
	if string(prompt) != want {
		t.Fatalf("prompt mismatch:\n%s", prompt)
	}
	if strings.Contains(string(prompt), "available_tools") {
		t.Fatalf("prompt exposed a command inventory:\n%s", prompt)
	}
	for _, guidance := range []string{
		"Prefer shell builtins and standard utilities normally supplied with the stated operating system.",
		"If the user explicitly requests a particular tool, preserve that choice.",
		"do not assume optional third-party software is installed",
		"checks statically identifiable direct command names against the local target shell",
	} {
		if !strings.Contains(string(prompt), guidance) {
			t.Fatalf("prompt omitted guidance %q", guidance)
		}
	}
}
