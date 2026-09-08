// Package commandcheck extracts executable names from shell commands without
// evaluating those commands.
package commandcheck

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

type collector struct {
	names     []string
	seen      map[string]bool
	functions map[string]bool
}

// ExecutableNames returns statically identifiable direct command words in
// lexical order. It recognizes commands in pipelines, substitutions, and
// function bodies because those are represented as CallExpr nodes. Dynamic
// command words are deliberately skipped.
func ExecutableNames(command string) []string {
	file := parse(command)
	if file == nil {
		return nil
	}
	c := &collector{
		seen:      make(map[string]bool),
		functions: make(map[string]bool),
	}

	// Learn declarations first so a function call is not mistaken for a PATH
	// executable, even when its declaration appears later in the line.
	syntax.Walk(file, func(node syntax.Node) bool {
		declaration, ok := node.(*syntax.FuncDecl)
		if !ok {
			return true
		}
		if declaration.Name != nil {
			c.functions[declaration.Name.Value] = true
		}
		for _, name := range declaration.Names {
			c.functions[name.Value] = true
		}
		return true
	})

	syntax.Walk(file, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		name, static := staticWord(call.Args[0])
		if !static || name == "" || c.functions[name] || c.seen[name] {
			return true
		}
		c.seen[name] = true
		c.names = append(c.names, name)
		return true
	})
	return c.names
}

func parse(command string) *syntax.File {
	for _, language := range []syntax.LangVariant{syntax.LangBash, syntax.LangZsh} {
		file, err := syntax.NewParser(syntax.Variant(language)).Parse(strings.NewReader(command), "")
		if err == nil {
			return file
		}
	}
	return nil
}

func staticWord(word *syntax.Word) (string, bool) {
	var value strings.Builder
	if !appendStaticParts(&value, word.Parts, false) {
		return "", false
	}
	return value.String(), true
}

func appendStaticParts(value *strings.Builder, parts []syntax.WordPart, quoted bool) bool {
	for _, part := range parts {
		switch typed := part.(type) {
		case *syntax.Lit:
			if !quoted && (strings.ContainsAny(typed.Value, "*?[") || (value.Len() == 0 && strings.HasPrefix(typed.Value, "~"))) {
				return false
			}
			value.WriteString(typed.Value)
		case *syntax.SglQuoted:
			if typed.Dollar {
				return false
			}
			value.WriteString(typed.Value)
		case *syntax.DblQuoted:
			if typed.Dollar || !appendStaticParts(value, typed.Parts, true) {
				return false
			}
		default:
			return false
		}
	}
	return true
}
