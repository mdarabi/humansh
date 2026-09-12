package commandgrammar

import (
	"context"
	"strings"
	"testing"
)

func TestParseHelpCommonLayouts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		help         string
		commands     []string
		options      map[string]OptionSpec
		state        SubcommandState
		optionsKnown bool
	}{
		{
			name: "cobra",
			help: `Usage:
  fixturevcs [command]

Available Commands:
  commit      Record changes
  remote      Manage remotes
  status      Show status

Flags:
  -C, --directory string  run from a directory
      --color[=WHEN]      colorize output
  -h, --help              help for fixturevcs
`,
			commands: []string{"commit", "remote", "status"},
			options: map[string]OptionSpec{
				"-C":          {Value: RequiredValue, AllowSeparate: true},
				"--directory": {Value: RequiredValue, AllowSeparate: true},
				"--color":     {Value: OptionalValue, AllowAttached: true},
				"-h":          {Terminal: true},
				"--help":      {Terminal: true},
			},
			state: SubcommandsListed, optionsKnown: true,
		},
		{
			name: "git-like-common-commands",
			help: `usage: fixturevcs [--version] [--help] [-C <path>]
                  <command> [<args>]

These are common FixtureVCS commands used in various situations:
   commit     Record changes
   remote     Manage remotes
   status     Show status
`,
			commands: []string{"commit", "remote", "status"},
			options: map[string]OptionSpec{
				"--version": {Terminal: true},
				"--help":    {Terminal: true},
				"-C":        {Value: RequiredValue, AllowSeparate: true},
			},
			state: SubcommandsListed, optionsKnown: true,
		},
		{
			name: "argparse-choices-remain-unprobed",
			help: `usage: fixturevcs [-h] {commit,remote,status} ...

positional arguments:
  {commit,remote,status}

options:
  -h, --help  show this help message
`,
			options: map[string]OptionSpec{
				"-h": {Terminal: true}, "--help": {Terminal: true},
			},
			state: SubcommandsUnknown, optionsKnown: true,
		},
		{
			name: "click",
			help: `Usage: fixturevcs [OPTIONS] COMMAND [ARGS]...

Options:
  --config PATH  configuration file
  --help         show this message and exit

Commands:
  inspect  inspect state
  serve    run the service
`,
			commands: []string{"inspect", "serve"},
			options: map[string]OptionSpec{
				"--config": {Value: RequiredValue, AllowSeparate: true},
				"--help":   {Terminal: true},
			},
			state: SubcommandsListed, optionsKnown: true,
		},
		{
			name: "clap",
			help: `Usage: fixturevcs [OPTIONS] <COMMAND>

Commands:
  inspect  inspect state
  serve    run the service

Options:
  -q, --quiet        quiet output
      --jobs <COUNT> worker count
  -h, --help         Print help
`,
			commands: []string{"inspect", "serve"},
			options: map[string]OptionSpec{
				"-q": {}, "--quiet": {},
				"--jobs": {Value: RequiredValue, AllowSeparate: true},
				"-h":     {Terminal: true}, "--help": {Terminal: true},
			},
			state: SubcommandsListed, optionsKnown: true,
		},
		{
			name: "go-tool",
			help: `Go is a tool for managing source code.

Usage:
  gotool <command> [arguments]

The commands are:
  build       compile packages
  test        test packages
`,
			commands:     []string{"build", "test"},
			state:        SubcommandsListed,
			optionsKnown: true,
		},
		{
			name: "title-case-command-groups-and-bare-rows",
			help: `Usage: fixturectl [OPTIONS] COMMAND

Basic Commands (Beginner):
  create
  get       display resources

Other Commands:
  inspect, explain

Examples:
  create  is an example sentence, not another command
`,
			commands:     []string{"create", "get", "inspect", "explain"},
			state:        SubcommandsListed,
			optionsKnown: false,
		},
		{
			name: "wrapped-command-list",
			help: `Usage: package-tool <command>

All commands:
  access, adduser, audit,
  cache, ci, completion
`,
			commands:     []string{"access", "adduser", "audit", "cache", "ci", "completion"},
			state:        SubcommandsListed,
			optionsKnown: true,
		},
		{
			name: "man-overstrikes-and-ansi",
			help: "S\bSY\bYN\bNO\bOP\bPS\bSI\bIS\n    fixturevcs [--format=<VALUE>] FILE\n\n\x1b[1mOPTIONS\x1b[0m\n    -m <TEXT>, --message=<TEXT>\n        message text\n",
			options: map[string]OptionSpec{
				"--format":  {Value: RequiredValue, AllowAttached: true},
				"-m":        {Value: RequiredValue, AllowSeparate: true},
				"--message": {Value: RequiredValue, AllowAttached: true},
			},
			state: SubcommandsNone, optionsKnown: true,
		},
		{
			name: "headerless-options-negated-alias-and-optional-short-value",
			help: `usage: fixturevcs operation [<options>] <name> <url>
    -f, --[no-]fetch                    fetch after adding
    -t <BRANCH>, --track <BRANCH>       track a branch
    -u[<MODE>], --untracked-files[=<MODE>]
`,
			options: map[string]OptionSpec{
				"-f":                {},
				"--fetch":           {},
				"--no-fetch":        {},
				"-t":                {Value: RequiredValue, AllowSeparate: true},
				"--track":           {Value: RequiredValue, AllowSeparate: true},
				"-u":                {Value: OptionalValue, AllowAttached: true},
				"--untracked-files": {Value: OptionalValue, AllowAttached: true},
			},
			state: SubcommandsNone, optionsKnown: true,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			node, err := ParseHelp([]byte(test.help), true)
			if err != nil {
				t.Fatal(err)
			}
			if node.SubcommandState != test.state || node.OptionsKnown != test.optionsKnown || !node.Complete {
				t.Fatalf("node metadata=%+v", node)
			}
			for _, command := range test.commands {
				if _, ok := node.Subcommands[command]; !ok {
					t.Errorf("missing command %q in %v", command, node.Subcommands)
				}
			}
			for name, want := range test.options {
				if got, ok := node.Options[name]; !ok || got != want {
					t.Errorf("option %s=%+v,%t; want %+v", name, got, ok, want)
				}
			}
		})
	}
}

func TestParseHelpRecognizesOnlyBoundedForwardedCommandTails(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, help string
		incomplete bool
		want       bool
	}{
		{name: "container-run", help: "Usage: box run [OPTIONS] IMAGE [COMMAND] [ARG...]", want: true},
		{name: "container-exec", help: "Usage: box exec [OPTIONS] CONTAINER COMMAND [ARG...]", want: true},
		{name: "wrapped", help: "Usage:\n  runner [FLAGS] <target>\n    <command> [<args>]...", want: true},
		{name: "multiple-required-operands", help: "Usage: runner [OPTIONS] HOST USER COMMAND [ARGS]...", want: true},
		{name: "root-subcommands", help: "Usage: box [OPTIONS] COMMAND [ARG...]"},
		{name: "listed-subcommands", help: "Usage: box [OPTIONS] TARGET COMMAND [ARG...]\n\nCommands:\n  status  Show status"},
		{name: "ordinary-operands", help: "Usage: box [OPTIONS] FILE [FILE...]"},
		{name: "optional-target", help: "Usage: box [OPTIONS] [TARGET] COMMAND [ARG...]"},
		{name: "variable-targets", help: "Usage: box [OPTIONS] TARGET... COMMAND [ARG...]"},
		{name: "interspersed-options", help: "Usage: box [OPTIONS] TARGET [--local] COMMAND [ARG...]"},
		{name: "ambiguous-alternatives", help: "Usage: box [OPTIONS] TARGET | FILE COMMAND [ARG...]"},
		{name: "no-argument-tail", help: "Usage: box [OPTIONS] TARGET COMMAND"},
		{name: "truncated-help", help: "Usage: box [OPTIONS] TARGET COMMAND [ARG...]", incomplete: true},
		{name: "conflicting-synopses", help: "Usage: box [OPTIONS] FILE\nUsage: box [OPTIONS] TARGET COMMAND [ARG...]"},
		{name: "prose-only", help: "Usage: box [OPTIONS] FILE\n\nFor example: box [OPTIONS] TARGET COMMAND [ARG...]"},
	} {
		t.Run(test.name, func(t *testing.T) {
			node, err := ParseHelp([]byte(test.help), !test.incomplete)
			if err != nil {
				t.Fatal(err)
			}
			if node.ForwardsCommand != test.want {
				t.Fatalf("ForwardsCommand=%t, want %t for %q", node.ForwardsCommand, test.want, test.help)
			}
		})
	}
}

func TestParseHelpKeepsCustomOptionTypesSeparateFromProse(t *testing.T) {
	t.Parallel()
	node, err := ParseHelp([]byte(`Usage: box [OPTIONS] FILE

Options:
  -n, --network network  Connect to a network
      --device gpu-request  Select a device
      --rm               Remove after exit
      --quiet silence
      --verbose print detailed output
`), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"-n", "--network", "--device"} {
		if got := node.Options[name]; got.Value != RequiredValue || !got.AllowSeparate {
			t.Errorf("custom option type was lost for %s: %+v", name, got)
		}
	}
	for _, name := range []string{"--rm", "--quiet", "--verbose"} {
		if got := node.Options[name]; got.Value != NoValue {
			t.Errorf("prose became a value for %s: %+v", name, got)
		}
	}
}

func TestParsedHelpAcceptsPositionalsAlongsideSubcommands(t *testing.T) {
	t.Parallel()
	root, err := ParseHelp([]byte(`Usage: zellij [OPTIONS] [COMMAND]

Commands:
  attach  Attach to a session
  help    Print this message or the help of the given subcommand(s)

Options:
  -h, --help  Print help
`), true)
	if err != nil {
		t.Fatal(err)
	}
	attach, err := ParseHelp([]byte(`Usage: zellij attach [OPTIONS] [SESSION_NAME] [-- <INITIAL_COMMAND>...] [COMMAND]

Commands:
  options  Change the behaviour of zellij
  help     Print this message or the help of the given subcommand(s)

Arguments:
  [SESSION_NAME]        Name of the session to attach to
  [INITIAL_COMMAND]...  Command to run in the first pane of the session, if it is created

Options:
  -c, --create  Create a session if one does not exist
  -h, --help    Print help
`), true)
	if err != nil {
		t.Fatal(err)
	}
	if root.AcceptsPositionals {
		t.Fatal("a command-only root was marked as accepting positional arguments")
	}
	if !attach.AcceptsPositionals {
		t.Fatal("the explicit Arguments section was not retained")
	}

	session := &fakeHelpSession{nodes: map[string]HelpResult{
		"":       {Node: root, Status: HelpOK},
		"attach": {Node: attach, Status: HelpOK},
	}}
	analysis := NewAnalyzer(&fakeHelpSource{session: session}).Analyze(context.Background(), invocation("zellij attach -c pyxis-codex -- codex"))
	if analysis.Coverage != CoverageRecognized || analysis.StopReason != StopComplete || analysis.Uncertain() {
		t.Fatalf("zellij attach analysis=%+v annotations=%+v", analysis, analysis.Annotations)
	}
	wantRoles := []Role{RoleHead, RoleSubcommand, RoleOption, RolePositional, RoleOption, RolePositional}
	for index, want := range wantRoles {
		if got := analysis.RoleAt(index); got != want {
			t.Errorf("role[%d]=%s, want %s", index, got, want)
		}
	}
	if got := strings.Join(session.calls, ","); got != ",attach" {
		t.Fatalf("help probes=%q, want root and attach only", got)
	}
}

func TestParseHelpExpandsBSDCompactShortOptions(t *testing.T) {
	t.Parallel()
	node, err := ParseHelp([]byte(`rm: illegal option -- -
usage: rm [-f | -i] [-dIPRrvWx] file ...
       unlink [--] file
`), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"-f", "-i", "-d", "-I", "-P", "-R", "-r", "-v", "-W", "-x"} {
		if option, exists := node.Options[name]; !exists || option != (OptionSpec{}) {
			t.Errorf("compact option %s=%+v,%t", name, option, exists)
		}
	}
	if _, exists := node.Options["-dIPRrvWx"]; !exists {
		t.Fatalf("compact usage atom's exact spelling was not preserved: %v", node.Options)
	}

	session := &fakeHelpSession{nodes: map[string]HelpResult{"": {Node: node, Status: HelpOK}}}
	analysis := NewAnalyzer(&fakeHelpSource{session: session}).Analyze(context.Background(), invocation("rm -rf internal/commandgrammar/"))
	if analysis.Coverage != CoverageRecognized || analysis.StopReason != StopComplete || analysis.Boundary != 3 || analysis.RoleAt(1) != RoleOption || analysis.RoleAt(2) != RolePositional {
		t.Fatalf("compact cluster analysis=%+v annotations=%+v", analysis, analysis.Annotations)
	}
	unknown := NewAnalyzer(&fakeHelpSource{session: &fakeHelpSession{nodes: map[string]HelpResult{"": {Node: node, Status: HelpOK}}}}).Analyze(context.Background(), invocation("rm -rz path"))
	if unknown.StopReason != StopUnknownOption || !unknown.Uncertain() {
		t.Fatalf("unknown compact member analysis=%+v", unknown)
	}
}

func TestParseHelpHandlesCommonBSDCompactShortOptionLayouts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		help        string
		invocation  string
		wantOptions []string
	}{
		{
			name: "find mixed compact set",
			help: `/usr/bin/find: illegal option -- -
usage: find [-H | -L | -P] [-EXdsx] [-f path] path ... [expression]
       find [-H | -L | -P] [-EXdsx] -f path [path ...] [expression]
`,
			invocation:  "find -ds .",
			wantOptions: []string{"-E", "-X", "-d", "-s", "-x"},
		},
		{
			name:        "ls symbol rich compact set",
			help:        "ls: unrecognized option `--help'\nusage: ls [-@ABCFGHILOPRSTUWXabcdefghiklmnopqrstuvwxy1%,] [--color=when] [-D format] [file ...]\n",
			invocation:  "ls -la .",
			wantOptions: []string{"-@", "-A", "-a", "-l", "-1", "-%", "-,"},
		},
		{
			name: "cp short alternative and longer compact set",
			help: `/bin/cp: illegal option -- -
usage: cp [-R [-H | -L | -P]] [-fi | -n] [-aclpSsvXx] source_file target_file
       cp [-R [-H | -L | -P]] [-fi | -n] [-aclpSsvXx] source_file ... target_directory
`,
			invocation:  "cp -if source target",
			wantOptions: []string{"-f", "-i", "-a", "-c", "-S", "-s", "-X", "-x"},
		},
		{
			name: "mv short set corroborated across usage lines",
			help: `/bin/mv: illegal option -- -
usage: mv [-f | -i | -n] [-hv] source target
       mv [-f | -i | -n] [-v] source ... directory
`,
			invocation:  "mv -vh source target",
			wantOptions: []string{"-h", "-v"},
		},
		{
			name: "mkdir short set with rejected long help",
			help: `/bin/mkdir: illegal option -- -
usage: mkdir [-pv] [-m mode] directory_name ...
`,
			invocation:  "mkdir -vp directory",
			wantOptions: []string{"-p", "-v"},
		},
		{
			name: "chmod short set with rejected long help",
			help: `/bin/chmod: illegal option -- -
usage: chmod [-fhv] [-R [-H | -L | -P]] mode file ...
`,
			invocation:  "chmod -hv file",
			wantOptions: []string{"-f", "-h", "-v"},
		},
		{
			name:        "du compact mixed set",
			help:        "du: unrecognized option `--help'\nusage: du [-Aclnx] [-H | -L | -P] [-g | -h | -k | -m] [-a | -s | -d depth] [file ...]\n",
			invocation:  "du -nx .",
			wantOptions: []string{"-A", "-c", "-l", "-n", "-x"},
		},
		{
			name:        "uniq four letter lowercase set",
			help:        "uniq: unrecognized option `--help'\nusage: uniq [-cdiu] [-D[septype]] [-f fields] [-s chars] [input [output]]\n",
			invocation:  "uniq -ud input",
			wantOptions: []string{"-c", "-d", "-i", "-u"},
		},
		{
			name: "ssh digit and letter set",
			help: `/usr/bin/ssh: illegal option -- -
usage: ssh [-46AaCfGgKkMNnqsTtVvXxYy] [-B bind_interface] [-b bind_address]
           destination [command [argument ...]]
`,
			invocation:  "ssh -4v example.com",
			wantOptions: []string{"-4", "-6", "-A", "-a", "-v"},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			node, err := ParseHelp([]byte(test.help), true)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range test.wantOptions {
				if option, exists := node.Options[name]; !exists || option != (OptionSpec{}) {
					t.Errorf("compact option %s=%+v,%t", name, option, exists)
				}
			}
			session := &fakeHelpSession{nodes: map[string]HelpResult{"": {Node: node, Status: HelpOK}}}
			analysis := NewAnalyzer(&fakeHelpSource{session: session}).Analyze(context.Background(), invocation(test.invocation))
			if analysis.Coverage != CoverageRecognized || analysis.StopReason != StopComplete {
				t.Fatalf("compact analysis=%+v annotations=%+v", analysis, analysis.Annotations)
			}
		})
	}
}

func TestParseHelpDoesNotSplitAmbiguousSingleDashWordsOrShareAlternativeValues(t *testing.T) {
	t.Parallel()
	node, err := ParseHelp([]byte(`Usage: tool [-q | -v] [-verbose] [-Ipath] [-fi] [-ABC] [-jN] [-j4] [-O2] [-sha256] [-fooBARBazQux FILE] [-noCacheByID] [-a | -o FILE] FILE

Options:
  -O | --output FILE  write output
  -noCacheByID        preserve exact single-dash option
`), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, exact := range []string{"-verbose", "-Ipath", "-fi", "-ABC", "-jN", "-j4", "-O2", "-sha256", "-fooBARBazQux", "-noCacheByID"} {
		if _, exists := node.Options[exact]; !exists {
			t.Errorf("ambiguous option spelling %q was not retained: %v", exact, node.Options)
		}
	}
	for _, derived := range []string{"-I", "-f", "-B", "-j", "-4", "-2", "-h", "-6", "-b", "-Q", "-n", "-C", "-y"} {
		if _, exists := node.Options[derived]; exists {
			t.Errorf("ambiguous option spelling derived %q: %v", derived, node.Options)
		}
	}
	if option := node.Options["-a"]; option.Value != NoValue {
		t.Fatalf("alternative -a inherited another option's value: %+v", option)
	}
	if option := node.Options["-o"]; option.Value != RequiredValue || !option.AllowSeparate {
		t.Fatalf("value-taking alternative -o=%+v", option)
	}
	if option := node.Options["-O"]; option.Value != RequiredValue || !option.AllowSeparate {
		t.Fatalf("pipe-separated option aliases did not share value grammar: %+v", option)
	}
	if option := node.Options["-fooBARBazQux"]; option.Value != RequiredValue || !option.AllowSeparate {
		t.Fatalf("mixed-case exact option lost its value grammar: %+v", option)
	}

	unbracketed, err := ParseHelp([]byte("Usage: tool -a | -o FILE\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	if option := unbracketed.Options["-a"]; option.Value != NoValue {
		t.Fatalf("unbracketed alternative -a inherited another option's value: %+v", option)
	}
	if option := unbracketed.Options["-o"]; option.Value != RequiredValue || !option.AllowSeparate {
		t.Fatalf("unbracketed value-taking alternative -o=%+v", option)
	}

	bareExact, err := ParseHelp([]byte("Usage: tool [-q | -v] [-noCacheByID]\n  -noCacheByID\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, derived := range []string{"-n", "-C", "-B", "-I", "-D"} {
		if _, exists := bareExact.Options[derived]; exists {
			t.Errorf("bare exact option row derived %q: %v", derived, bareExact.Options)
		}
	}
}

func TestParseHelpDoesNotPromoteArgumentChoicesOrExecutableSuffixes(t *testing.T) {
	t.Parallel()
	node, err := ParseHelp([]byte(`Usage: my-tool [--color {auto,always,never}] {json,yaml} FILE

Options:
  --color {auto,always,never}  color mode
`), true)
	if err != nil {
		t.Fatal(err)
	}
	if node.SubcommandState != SubcommandsUnknown {
		t.Fatalf("subcommand state=%v, want unknown positional structure", node.SubcommandState)
	}
	if len(node.Subcommands) != 0 {
		t.Fatalf("argument choices became executable subcommands: %v", node.Subcommands)
	}
	if _, exists := node.Options["-tool"]; exists {
		t.Fatalf("executable-name suffix became an option: %v", node.Options)
	}
	if _, exists := node.Options["--color"]; !exists {
		t.Fatalf("documented option missing: %v", node.Options)
	}

	plain, err := ParseHelp([]byte("Usage: my-tool FILE\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := plain.Options["-tool"]; exists {
		t.Fatalf("unbracketed executable-name suffix became an option: %v", plain.Options)
	}
}

func TestCommandDescriptionBraceEnumNeverBecomesAProbePrefix(t *testing.T) {
	t.Parallel()
	node, err := ParseHelp([]byte(`Usage: fixture [COMMAND]

Commands:
  render  output as {json,yaml}
`), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, undocumented := range []string{"json", "yaml"} {
		if _, exists := node.Subcommands[undocumented]; exists {
			t.Fatalf("description enum %q became a subcommand: %v", undocumented, node.Subcommands)
		}
	}

	session := &fakeHelpSession{nodes: map[string]HelpResult{
		"": {Node: node, Status: HelpOK},
	}}
	analysis := NewAnalyzer(&fakeHelpSource{session: session}).Analyze(context.Background(), invocation("fixture json please"))
	if analysis.StopReason != StopUndocumentedSubcommand || analysis.Boundary != 1 {
		t.Fatalf("analysis=%+v", analysis)
	}
	if got := strings.Join(session.calls, ","); got != "" {
		t.Fatalf("description enum reached a nested help probe: %q", got)
	}
}

func TestWrappedCommandDescriptionNeverBecomesAProbePrefix(t *testing.T) {
	t.Parallel()
	node, err := ParseHelp([]byte(`Usage: fixture [COMMAND]

Commands:
  render  Render output in a format that may wrap
          across  multiple lines
  status  Show status
`), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"render", "status"} {
		if _, exists := node.Subcommands[command]; !exists {
			t.Fatalf("documented command %q missing: %v", command, node.Subcommands)
		}
	}
	if _, exists := node.Subcommands["across"]; exists {
		t.Fatalf("wrapped description became a subcommand: %v", node.Subcommands)
	}

	session := &fakeHelpSession{nodes: map[string]HelpResult{
		"": {Node: node, Status: HelpOK},
	}}
	analysis := NewAnalyzer(&fakeHelpSource{session: session}).Analyze(context.Background(), invocation("fixture across please"))
	if analysis.StopReason != StopUndocumentedSubcommand || analysis.Boundary != 1 {
		t.Fatalf("analysis=%+v", analysis)
	}
	if got := strings.Join(session.calls, ","); got != "" {
		t.Fatalf("wrapped description reached a nested help probe: %q", got)
	}
}

func TestCommandSentenceNeverOpensAProbeableSection(t *testing.T) {
	t.Parallel()
	node, err := ParseHelp([]byte(`Usage: fixture FILE

Commands may be written across multiple lines.
  across  multiple examples
`), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := node.Subcommands["across"]; exists {
		t.Fatalf("prose sentence opened a command section: %v", node.Subcommands)
	}

	session := &fakeHelpSession{nodes: map[string]HelpResult{
		"": {Node: node, Status: HelpOK},
	}}
	analysis := NewAnalyzer(&fakeHelpSource{session: session}).Analyze(context.Background(), invocation("fixture across please"))
	if analysis.RoleAt(1) != RolePositional {
		t.Fatalf("prose-derived word was not left positional: %+v", analysis)
	}
	if got := strings.Join(session.calls, ","); got != "" {
		t.Fatalf("prose-derived word reached a nested help probe: %q", got)
	}
}

func TestParseHelpDistinguishesCompleteAndCommonCommandLists(t *testing.T) {
	t.Parallel()
	complete, err := ParseHelp([]byte("Usage: tool COMMAND\n\nAvailable Commands:\n  run  run it\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	partial, err := ParseHelp([]byte("Usage: tool COMMAND\n\nThese are common tool commands:\n  run  run it\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	if !complete.SubcommandsComplete || partial.SubcommandsComplete {
		t.Fatalf("complete=%t common=%t", complete.SubcommandsComplete, partial.SubcommandsComplete)
	}
}

func TestParseHelpRejectsProseAndBinaryData(t *testing.T) {
	t.Parallel()
	for _, value := range [][]byte{
		[]byte("This prose happens to mention --force and the status command."),
		[]byte("Usage: fixture\x00hidden"),
		[]byte(strings.Repeat("x", maxHelpParseBytes+1)),
	} {
		if node, err := ParseHelp(value, true); err == nil {
			t.Fatalf("ParseHelp accepted unsupported input: %+v", node)
		}
	}
}

func TestParseHelpMarksTruncatedOutputIncomplete(t *testing.T) {
	t.Parallel()
	node, err := ParseHelp([]byte("Usage: fixture [--help] FILE\n"), false)
	if err != nil || node.Complete {
		t.Fatalf("node=%+v err=%v", node, err)
	}
}

func TestParseHelpRecognizesDynamicBareCommandMetavar(t *testing.T) {
	t.Parallel()
	node, err := ParseHelp([]byte("Usage: fixture [OPTIONS] COMMAND [ARGS]...\n"), true)
	if err != nil || node.SubcommandState != SubcommandsUnknown {
		t.Fatalf("node=%+v err=%v", node, err)
	}
}

func TestParseHelpLeavesOpaqueUsageFlagsIncomplete(t *testing.T) {
	t.Parallel()
	node, err := ParseHelp([]byte("usage: go test [build/test flags] [packages] [build/test flags & test binary flags]\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	if node.OptionsKnown {
		t.Fatalf("opaque flag groups were treated as an exhaustive option list: %+v", node)
	}

	root := NodeSpec{
		OptionsKnown:        true,
		Options:             map[string]OptionSpec{},
		SubcommandState:     SubcommandsListed,
		Subcommands:         map[string]struct{}{"test": {}},
		SubcommandsComplete: true,
		Complete:            true,
	}
	session := &fakeHelpSession{nodes: map[string]HelpResult{
		"":     {Node: root, Status: HelpOK},
		"test": {Node: node, Status: HelpOK},
	}}
	analysis := NewAnalyzer(&fakeHelpSource{session: session}).Analyze(context.Background(), invocation("go test -cover"))
	if analysis.Coverage != CoveragePartial || analysis.StopReason != StopComplete || analysis.Uncertain() || analysis.RoleAt(2) != RoleOption {
		t.Fatalf("go test -cover analysis=%+v annotations=%+v", analysis, analysis.Annotations)
	}
}

func TestParseHelpRecognizesDocumentedHelpFormWithoutProbingIt(t *testing.T) {
	t.Parallel()
	node, err := ParseHelp([]byte(`Go is a tool for managing Go source code.

Usage:

	go <command> [arguments]

The commands are:

	build       compile packages and dependencies
	test        test packages

Use "go help <command>" for more information about a command.

Additional help topics:

	packages    package lists and patterns

Use "go help <topic>" for more information about that topic.
`), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := node.Subcommands["help"]; !exists {
		t.Fatalf("documented help form was not retained as a subcommand: %+v", node)
	}

	session := &fakeHelpSession{nodes: map[string]HelpResult{
		"": {Node: node, Status: HelpOK},
	}}
	analysis := NewAnalyzer(&fakeHelpSource{session: session}).Analyze(context.Background(), invocation("go help test"))
	if analysis.Coverage != CoveragePartial || analysis.StopReason != StopComplete || analysis.Uncertain() {
		t.Fatalf("go help test analysis=%+v annotations=%+v", analysis, analysis.Annotations)
	}
	wantRoles := []Role{RoleHead, RolePositional, RolePositional}
	for index, want := range wantRoles {
		if got := analysis.RoleAt(index); got != want {
			t.Errorf("role[%d]=%s, want %s", index, got, want)
		}
	}
	if got := strings.Join(session.calls, ","); got != "" {
		t.Fatalf("documented positional help was executed as a nested probe: %q", got)
	}
}

func FuzzParseHelpNeverPanics(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte("Usage: fixture [OPTIONS] <COMMAND>\nCommands:\n  run  run it\n"),
		[]byte("OPTIONS\n  -m <TEXT>, --message=<TEXT>\n"),
		[]byte("\x1b[31mUsage:\x1b[0m fixture FILE\n"),
		{0, 1, 2, 3},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value []byte) {
		if len(value) > maxHelpParseBytes+1 {
			t.Skip()
		}
		_, _ = ParseHelp(value, true)
	})
}
