package commandcheck

import (
	"reflect"
	"testing"
)

func TestExecutableNamesFindsStaticCommandsWithoutTreatingArgumentsAsCommands(t *testing.T) {
	t.Parallel()
	command := `helper() { curl -s "$URL" | jq .; }
result=$(awk '{print $1}' input.txt)
rg pattern .
helper`
	want := []string{"curl", "jq", "awk", "rg"}
	if got := ExecutableNames(command); !reflect.DeepEqual(got, want) {
		t.Fatalf("ExecutableNames()=%v want %v", got, want)
	}
}

func TestExecutableNamesTreatsCommandArgumentsAsData(t *testing.T) {
	t.Parallel()
	command := `printf '%s\n' curl
find . -name jq`
	want := []string{"printf", "find"}
	if got := ExecutableNames(command); !reflect.DeepEqual(got, want) {
		t.Fatalf("ExecutableNames()=%v want %v", got, want)
	}
}

func TestExecutableNamesSkipsDynamicHeadsAndNameQueries(t *testing.T) {
	t.Parallel()
	command := `"$TOOL" --version
	./tools/* --version
command -v optional-tool`
	want := []string{"command"}
	if got := ExecutableNames(command); !reflect.DeepEqual(got, want) {
		t.Fatalf("ExecutableNames()=%v want %v", got, want)
	}
}

func TestExecutableNamesUsesTheZshParserFallback(t *testing.T) {
	t.Parallel()
	want := []string{"print"}
	if got := ExecutableNames("(){ print -r -- zsh-only; }"); !reflect.DeepEqual(got, want) {
		t.Fatalf("Zsh fallback names=%v want %v", got, want)
	}
}
