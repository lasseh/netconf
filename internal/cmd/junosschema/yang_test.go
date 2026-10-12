package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/carlmjohnson/be"
)

// written puts statements back into one line, each argument between
// brackets.
func written(statements []*stmt) string {
	var b strings.Builder
	for _, s := range statements {
		fmt.Fprintf(&b, "%s[%s]", s.keyword, s.arg)
		if len(s.sub) > 0 {
			fmt.Fprintf(&b, "{%s}", written(s.sub))
		}
		b.WriteString(" ")
	}
	return strings.TrimSpace(b.String())
}

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"nothing", " \n", ""},
		{"no argument", "input;", "input[]"},
		{"nested", "container a { leaf b { type string; } }", "container[a]{leaf[b]{type[string]}}"},
		{"a path as the argument", "augment /jc:configuration { uses x; }", "augment[/jc:configuration]{uses[x]}"},
		{"comments", "// one\nleaf a; /* two\n leaf b; */ leaf c; // three", "leaf[a] leaf[c]"},
		{"a comment that ends where it starts", "/*/ leaf a; */ leaf b;", "leaf[b]"},
		{"punctuation in a string", `description "a { b; } // c /* d";`, "description[a { b; } // c /* d]"},
		{"an escaped quote", `description "say \"x\" \\";`, `description[say \"x\" \\]`},
		{"single quotes", `pattern 'a"b\';`, `pattern[a"b\]`},
		{"strings joined", "description \"a\" +\n \"b\" + 'c';", "description[abc]"},
		{"an empty statement", "container a { }", "container[a]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			statements, err := parse([]byte(tt.src))
			be.NilErr(t, err)
			be.Equal(t, tt.want, written(statements))
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"a statement left open", "container a {\n leaf b;\n", "line 3: the file ends inside a statement"},
		{"no end of a statement", "leaf a", "line 1: the file ends inside a statement"},
		{"a brace too many", "leaf a;\n}", "line 2: } where a statement starts"},
		{"two arguments", "leaf a b;", "line 1: b after the argument of leaf"},
		{"a string left open", "leaf a;\ndescription \"b;\n", "line 2: a string is not closed"},
		{"a comment left open", "leaf a;\n\n/* b", "line 3: a comment is not closed"},
		{"nothing to join", `description "a" +`, "line 1: the file ends inside a statement"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parse([]byte(tt.src))
			be.Nonzero(t, err)
			be.Equal(t, tt.want, err.Error())
		})
	}
}

func TestParseModule(t *testing.T) {
	m, err := parseModule([]byte(`module junos-conf-system {
		revision 2025-01-01 { description "Junos: 25.4R1.12"; }
		revision 2024-01-01 { description "Junos: 24.4R1.9"; }
		grouping a { leaf b { type string; } }
	}`))
	be.NilErr(t, err)
	be.Equal(t, "junos-conf-system", m.name)
	be.Equal(t, "25.4R1.12", m.release)
	be.Equal(t, 1, len(m.groupings))

	m, err = parseModule([]byte(`module other { revision 2025-01-01 { description "First"; } }`))
	be.NilErr(t, err)
	be.Equal(t, "", m.release)

	for _, src := range []string{"", "submodule a { }", "module a { } module b { }", "module a {"} {
		_, err = parseModule([]byte(src))
		be.Nonzero(t, err)
	}
}

func TestDataNodes(t *testing.T) {
	m, err := parseModule([]byte(`module m {
		container top {
			description "not a data node";
			uses common;
			leaf plain { type string; }
			choice which {
				case one { leaf first { type string; } }
				leaf second { type string; }
			}
			container inner {
				uses common;
				uses inherited;
				list entry { key name; leaf name { type string; } }
			}
		}
		container broken { uses missing; }
		grouping common { leaf-list shared { type string; } uses deeper; }
		grouping deeper { anyxml anything; }
		grouping inherited { leaf-list apply-groups { type string; } container apply-macro { leaf name { type string; } } }
	}`))
	be.NilErr(t, err)
	top, broken := m.body[0], m.body[1]

	nodes, err := m.children(top)
	be.NilErr(t, err)
	names := make([]string, 0, len(nodes))
	for _, n := range nodes {
		names = append(names, n.keyword+" "+n.arg)
	}
	be.AllEqual(t, []string{"leaf-list shared", "anyxml anything", "leaf plain", "leaf first", "leaf second", "container inner"}, names)

	entry, err := m.descend(top, "inner", "entry")
	be.NilErr(t, err)
	be.Equal(t, "list", entry.keyword)
	same, err := m.descend(top)
	be.NilErr(t, err)
	be.Equal(t, top, same)
	_, err = m.descend(top, "inner", "nothing")
	be.True(t, errors.Is(err, errNotDeclared))

	paths, err := m.paths(top, "")
	be.NilErr(t, err)
	be.AllEqual(t, []string{"shared", "anything", "plain", "first", "second", "inner", "inner shared", "inner anything", "inner entry", "inner entry name"}, paths)

	// a grouping of another module, which nothing here reads
	_, err = m.children(broken)
	be.Equal(t, "module m has no grouping missing", err.Error())
	_, err = m.descend(broken, "anything")
	be.False(t, errors.Is(err, errNotDeclared))
	_, err = m.paths(m.body[0], "")
	be.NilErr(t, err)
}
