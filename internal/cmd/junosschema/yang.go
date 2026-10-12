package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// A stmt is a YANG statement: its keyword, its argument and the statements
// between its braces.
type stmt struct {
	keyword string
	arg     string
	sub     []*stmt
}

// parse reads the statements of a YANG file.  It reads the syntax of RFC
// 7950 and none of its meaning, and keeps a quoted argument as it is
// written, escapes included.
func parse(src []byte) ([]*stmt, error) {
	p := &parser{src: src}
	return p.statements(false)
}

type parser struct {
	src []byte
	pos int
}

// A token is a string or, when it is not quoted, one of "{", "}", ";" and
// the "+" that joins two strings.
type token struct {
	text   string
	quoted bool
}

func (t token) is(punctuation string) bool {
	return !t.quoted && t.text == punctuation
}

// statements reads statements up to the end of the file, or inside a
// statement up to the brace that closes it.
func (p *parser) statements(inside bool) ([]*stmt, error) {
	var found []*stmt
	for {
		tok, err := p.next()
		switch {
		case errors.Is(err, io.EOF) && !inside:
			return found, nil
		case err != nil:
			return nil, p.failed(err)
		case tok.is("}") && inside:
			return found, nil
		case tok.is("{"), tok.is("}"), tok.is(";"):
			return nil, p.errorf("%s where a statement starts", tok.text)
		}
		s := &stmt{keyword: tok.text}

		if tok, err = p.next(); err != nil {
			return nil, p.failed(err)
		}
		if !tok.is("{") && !tok.is(";") {
			s.arg = tok.text
			if tok, err = p.next(); err != nil {
				return nil, p.failed(err)
			}
			for tok.is("+") {
				var part token
				if part, err = p.next(); err != nil {
					return nil, p.failed(err)
				}
				s.arg += part.text
				if tok, err = p.next(); err != nil {
					return nil, p.failed(err)
				}
			}
		}

		switch {
		case tok.is(";"):
		case tok.is("{"):
			if s.sub, err = p.statements(true); err != nil {
				return nil, err
			}
		default:
			return nil, p.errorf("%s after the argument of %s", tok.text, s.keyword)
		}
		found = append(found, s)
	}
}

// next returns the next token, and io.EOF after the last.
func (p *parser) next() (token, error) {
	if err := p.skip(); err != nil {
		return token{}, err
	}
	if p.pos == len(p.src) {
		return token{}, io.EOF
	}

	start := p.pos
	switch c := p.src[p.pos]; c {
	case '{', '}', ';':
		p.pos++
		return token{text: string(c)}, nil
	case '"', '\'':
		for p.pos++; p.pos < len(p.src) && p.src[p.pos] != c; p.pos++ {
			// a backslash only escapes between double quotes
			if c == '"' && p.src[p.pos] == '\\' {
				p.pos++
			}
		}
		if p.pos >= len(p.src) {
			p.pos = start
			return token{}, p.errorf("a string is not closed")
		}
		p.pos++
		return token{text: string(p.src[start+1 : p.pos-1]), quoted: true}, nil
	}
	for p.pos < len(p.src) && !strings.ContainsRune(" \t\r\n{};", rune(p.src[p.pos])) {
		p.pos++
	}
	return token{text: string(p.src[start:p.pos])}, nil
}

// skip moves over white space and comments.
func (p *parser) skip() error {
	for p.pos < len(p.src) {
		rest := p.src[p.pos:]
		switch {
		case strings.ContainsRune(" \t\r\n", rune(rest[0])):
			p.pos++
		case bytes.HasPrefix(rest, []byte("//")):
			end := bytes.IndexByte(rest, '\n')
			if end < 0 {
				end = len(rest)
			}
			p.pos += end
		case bytes.HasPrefix(rest, []byte("/*")):
			end := bytes.Index(rest[2:], []byte("*/"))
			if end < 0 {
				return p.errorf("a comment is not closed")
			}
			p.pos += 2 + end + 2
		default:
			return nil
		}
	}
	return nil
}

// failed is the error for a token that could not be read where one has to
// follow.
func (p *parser) failed(err error) error {
	if errors.Is(err, io.EOF) {
		return p.errorf("the file ends inside a statement")
	}
	return err
}

func (p *parser) errorf(format string, args ...any) error {
	line := 1 + bytes.Count(p.src[:p.pos], []byte("\n"))
	return fmt.Errorf("line %d: %s", line, fmt.Sprintf(format, args...))
}

// releasePrefix starts the description of the revision of a module of
// Junos: "Junos: 25.4R1.12".
const releasePrefix = "Junos: "

// A module is a YANG module of Junos.
type module struct {
	name      string // junos-conf-system
	release   string // 25.4R1.12, "" for a module that names none
	body      []*stmt
	groupings map[string]*stmt
}

// parseModule reads a file that holds one module.
func parseModule(src []byte) (*module, error) {
	top, err := parse(src)
	if err != nil {
		return nil, err
	}
	if len(top) != 1 || top[0].keyword != "module" {
		return nil, errors.New("not a YANG module")
	}

	m := &module{name: top[0].arg, body: top[0].sub, groupings: map[string]*stmt{}}
	for _, s := range m.body {
		switch s.keyword {
		case "grouping":
			m.groupings[s.arg] = s
		case "revision":
			for _, sub := range s.sub {
				release, ok := strings.CutPrefix(sub.arg, releasePrefix)
				if ok && sub.keyword == "description" && m.release == "" {
					m.release = release
				}
			}
		}
	}
	return m, nil
}

// children returns the data nodes right under s.  It looks through what
// only groups them: a choice with its cases, and a grouping that s uses.
func (m *module) children(s *stmt) ([]*stmt, error) {
	var nodes []*stmt
	for _, sub := range s.sub {
		grouped := sub
		switch sub.keyword {
		case "container", "list", "leaf", "leaf-list", "anyxml", "anydata":
			nodes = append(nodes, sub)
			continue
		case "choice", "case":
		case "uses":
			var ok bool
			if grouped, ok = m.groupings[sub.arg]; !ok {
				return nil, fmt.Errorf("module %s has no grouping %s", m.name, sub.arg)
			}
		default:
			continue
		}
		inner, err := m.children(grouped)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, inner...)
	}
	return nodes, nil
}

// errNotDeclared is the error of descend for a data node that is not there.
var errNotDeclared = errors.New("not declared")

// descend follows the names of data nodes down from s.
func (m *module) descend(s *stmt, path ...string) (*stmt, error) {
	for _, name := range path {
		nodes, err := m.children(s)
		if err != nil {
			return nil, err
		}
		i := slices.IndexFunc(nodes, func(n *stmt) bool { return n.arg == name })
		if i < 0 {
			return nil, fmt.Errorf("%s is %w in module %s", name, errNotDeclared, m.name)
		}
		s = nodes[i]
	}
	return s, nil
}

// inherited are the statements that every container of the Junos
// configuration takes, for its configuration groups.
var inherited = []string{"apply-groups", "apply-groups-except", "apply-macro"}

// paths returns every data node below s as the names that lead to it, but
// for the inherited ones.
func (m *module) paths(s *stmt, prefix string) ([]string, error) {
	nodes, err := m.children(s)
	if err != nil {
		return nil, err
	}
	var found []string
	for _, n := range nodes {
		if slices.Contains(inherited, n.arg) {
			continue
		}
		below, err := m.paths(n, prefix+n.arg+" ")
		if err != nil {
			return nil, err
		}
		found = append(append(found, prefix+n.arg), below...)
	}
	return found, nil
}
