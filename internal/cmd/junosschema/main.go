// Command junosschema compares the YANG modules of Junos releases with what
// the library depends on, to tell whether a release changes NETCONF for it.
// `make junos-schema` runs it over the modules Juniper publishes at
// github.com/Juniper/yang.
//
// It is given directories and reads two modules of every release it finds
// in them: junos-conf-system and junos-rpc-system, which have the name of
// the family in them for a family that has one (junos-ex-conf-system).  A
// module says itself which release it is of, so the modules a device hands
// out are compared like those Juniper publishes, and have to say the same
// where both are there.  From the first it lists the
// statements under "system services netconf" and what each release adds to
// them or takes away, which is how the NETCONF service of a release can be
// set up.  In the second it looks for the elements that package junos reads
// from a reply, and fails when a release does not declare one of them.
//
// That covers a part of what the library depends on.  The replies of
// <get-system-information> and <get-commit-information> have a model, from
// the release on that declares more than anyxml for them.  Nothing else
// that package junos sends or reads has one: <get-configuration>,
// <load-configuration>, <commit-configuration>, <open-configuration>,
// <close-configuration>, <command> and <get-database-status-information>.
// Neither have attributes, which YANG does not describe, such as the seconds
// of a <date-time>.  And no module says how a device behaves.  For all of
// that there is the integration suite.
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: junosschema <directory or file with YANG modules of Junos>...")
		os.Exit(2)
	}
	found, err := run(os.Args[1:])
	fmt.Print(found)
	if err != nil {
		fmt.Fprintln(os.Stderr, "junosschema:", err)
		os.Exit(1)
	}
}

// A reply is a reply the library decodes that Juniper publishes a model of:
// the request, the names from its output down to the element the library
// reads, and the children it reads there.
type reply struct {
	rpc      string
	path     []string
	elements []string
}

// replies has what package junos reads: SystemInformation in oper.go and
// the entry of GetCommitInformation in history.go.  Keep it in step with
// their struct tags.
var replies = []reply{
	{
		rpc:      "get-system-information",
		path:     []string{"system-information"},
		elements: []string{"host-name", "hardware-model", "os-name", "os-version", "serial-number"},
	},
	{
		rpc:      "get-commit-information",
		path:     []string{"commit-information", "commit-history"},
		elements: []string{"sequence-number", "user", "client", "date-time", "log", "comment", "commit-confirmed"},
	},
}

// What a release says about a reply, but for the elements it lacks.
const (
	replyComplete = "declares every element the library reads"
	replyNoModel  = "has no model of the reply"
)

// declared says what rpc module m declares of r, and returns an error for a
// reply that lacks what the library reads.
func (r reply) declared(m *module) (string, error) {
	i := slices.IndexFunc(m.body, func(s *stmt) bool { return s.keyword == "rpc" && s.arg == r.rpc })
	if i < 0 {
		return "", fmt.Errorf("module %s has no rpc %s", m.name, r.rpc)
	}
	rpc := m.body[i]
	if i = slices.IndexFunc(rpc.sub, func(s *stmt) bool { return s.keyword == "output" }); i < 0 {
		return "", fmt.Errorf("rpc %s of module %s has no output", r.rpc, m.name)
	}

	at := rpc.sub[i]
	for _, name := range r.path {
		var err error
		if at, err = m.descend(at, name); err != nil {
			return "", fmt.Errorf("the reply of %s: %w", r.rpc, err)
		}
		// a release that says no more than that the element is there
		if at.keyword == "anyxml" || at.keyword == "anydata" {
			return replyNoModel, nil
		}
	}

	nodes, err := m.children(at)
	if err != nil {
		return "", err
	}
	var missing []string
	for _, element := range r.elements {
		if !slices.ContainsFunc(nodes, func(n *stmt) bool { return n.arg == element }) {
			missing = append(missing, element)
		}
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("the reply of %s has no %s", r.rpc, strings.Join(missing, ", "))
	}
	return replyComplete, nil
}

// netconfStatements returns the statements under "system services netconf"
// that configuration module m declares, sorted.
func netconfStatements(m *module) ([]string, error) {
	for _, top := range m.body {
		netconf, err := m.descend(top, "system", "services", "netconf")
		if errors.Is(err, errNotDeclared) {
			continue
		}
		if err != nil {
			return nil, err
		}
		found, err := m.paths(netconf, "")
		slices.Sort(found)
		return found, err
	}
	return nil, fmt.Errorf("module %s declares no system services netconf", m.name)
}

// A release is what the two modules of one Junos release say.
type release struct {
	version    string
	conf, rpc  string   // the files of the modules, "" for one that was not found
	statements []string // under system services netconf
	replies    []string // what the release says about each of replies
}

// A comparison is the releases found, by family, and what is wrong with
// them.
type comparison struct {
	families map[string]map[string]*release
	problems []string
}

// systemModule matches the names of the two modules that are read and has
// the family and the kind of each.
var systemModule = regexp.MustCompile(`^(junos(?:-[a-z]+)?)-(conf|rpc)-system$`)

// evolved ends the release of a module of Junos OS Evolved, which has the
// module names of the junos family and is a family of its own here.
const evolved = "-EVO"

// add reads the module in a file and keeps what it says, if it is one of
// the two modules of a release.
func (c *comparison) add(path string) {
	src, err := os.ReadFile(path) //nolint:gosec // the files of whoever runs the command
	if err != nil {
		c.problem("%v", err)
		return
	}
	m, err := parseModule(src)
	if err != nil {
		c.problem("%s: %v", path, err)
		return
	}
	name := systemModule.FindStringSubmatch(m.name)
	if name == nil {
		return
	}
	if m.release == "" {
		c.problem("%s: module %s does not say which release of Junos it is of", path, m.name)
		return
	}

	family, kind := name[1], name[2]
	if strings.HasSuffix(m.release, evolved) {
		family += " evolved"
	}
	if c.families[family] == nil {
		c.families[family] = map[string]*release{}
	}
	r := c.families[family][m.release]
	if r == nil {
		r = &release{version: m.release, replies: make([]string, len(replies))}
		c.families[family][m.release] = r
	}

	// A release may come twice, as published and from a device.  Then both
	// have to say the same.
	differs := func(other string) {
		c.problem("%s %s: module %s in %s does not say what the one in %s says", family, m.release, m.name, path, other)
	}

	if kind == "conf" {
		statements, err := netconfStatements(m)
		if err != nil {
			c.problem("%s %s: %v", family, m.release, err)
		}
		switch {
		case r.conf == "":
			r.conf, r.statements = path, statements
		case !slices.Equal(statements, r.statements):
			differs(r.conf)
		}
		return
	}

	says := make([]string, len(replies))
	for i, reply := range replies {
		if says[i], err = reply.declared(m); err != nil {
			says[i] = "FAILS: " + err.Error()
			c.problem("%s %s: %v", family, m.release, err)
		}
	}
	switch {
	case r.rpc == "":
		r.rpc, r.replies = path, says
	case !slices.Equal(says, r.replies):
		differs(r.rpc)
	}
}

func (c *comparison) problem(format string, args ...any) {
	c.problems = append(c.problems, fmt.Sprintf(format, args...))
}

// run compares the modules under the directories and returns what it
// finds, with an error when a release lacks what the library reads or when
// the modules are not what it takes them for.
func run(dirs []string) (string, error) {
	w := &strings.Builder{}
	c := &comparison{families: map[string]map[string]*release{}}
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && filepath.Ext(path) == ".yang" {
				c.add(path)
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	if len(c.families) == 0 {
		c.problem("no junos-conf-system or junos-rpc-system module under %s", strings.Join(dirs, ", "))
	}

	for _, family := range slices.Sorted(maps.Keys(c.families)) {
		releases := slices.SortedFunc(maps.Values(c.families[family]), func(a, b *release) int {
			return compareVersions(a.version, b.version)
		})
		for _, r := range releases {
			if r.conf == "" {
				c.problem("%s %s: no configuration module", family, r.version)
			}
			if r.rpc == "" {
				c.problem("%s %s: no rpc module", family, r.version)
			}
		}
		report(w, family, releases)
	}

	for _, problem := range c.problems {
		fmt.Fprintln(w, "problem:", problem)
	}
	if len(c.problems) > 0 {
		return w.String(), fmt.Errorf("problems: %d", len(c.problems))
	}
	return w.String(), nil
}

// report writes what the releases of a family say, oldest first: the
// statements under system services netconf of the first and what each
// later one adds and takes away, and for each reply the runs of releases
// that say the same about it.
func report(w *strings.Builder, family string, releases []*release) {
	first, last := releases[0], releases[len(releases)-1]
	if first == last {
		fmt.Fprintf(w, "%s: %s\n", family, first.version)
	} else {
		fmt.Fprintf(w, "%s: %d releases, %s to %s\n", family, len(releases), first.version, last.version)
	}

	fmt.Fprintln(w, "\n  system services netconf")
	var before *release
	for _, r := range releases {
		if r.conf == "" {
			continue
		}
		var changes []string
		if before != nil {
			changes = slices.Concat(
				changed("+ ", r.statements, before.statements),
				changed("- ", before.statements, r.statements),
			)
		}
		if before == nil || r == last || len(changes) > 0 {
			fmt.Fprintf(w, "    %s: %d statements\n", r.version, len(r.statements))
		}
		for _, change := range changes {
			fmt.Fprintln(w, "      "+change)
		}
		before = r
	}

	for i, reply := range replies {
		fmt.Fprintf(w, "\n  %s\n", reply.rpc)
		for from := 0; from < len(releases); {
			to := from
			for to+1 < len(releases) && releases[to+1].replies[i] == releases[from].replies[i] {
				to++
			}
			span := releases[from].version
			if to > from {
				span += " to " + releases[to].version
			}
			says := releases[from].replies[i]
			if says == "" {
				says = "has no rpc module"
			}
			fmt.Fprintf(w, "    %s: %s\n", span, says)
			from = to + 1
		}
	}
	fmt.Fprintln(w)
}

// changed returns the statements of has that are not in lacks, each after
// the sign.  Both are sorted.  A statement that changes with everything
// under it is one line that counts what is under it.
func changed(sign string, has, lacks []string) []string {
	var only []string
	for _, statement := range has {
		if _, found := slices.BinarySearch(lacks, statement); !found {
			only = append(only, statement)
		}
	}

	var lines []string
	for i := 0; i < len(only); i++ {
		under := 0
		for i+under+1 < len(only) && strings.HasPrefix(only[i+under+1], only[i]+" ") {
			under++
		}
		line := sign + only[i]
		if under > 0 {
			line += fmt.Sprintf(" and %d under it", under)
		}
		lines = append(lines, line)
		i += under
	}
	return lines
}

// The parts of a release: 25.4R1.12 is build 12 of 25.4R1, and 25.4R1-S1.7
// build 7 of its first service release, which comes after it.
var (
	releaseParts = regexp.MustCompile(`^(\d+)\.(\d+)R(\d+)(?:-S(\d+))?(?:\.(\d+))?`)
	number       = regexp.MustCompile(`\d+`)
)

// compareVersions orders releases by their parts, so that 9.6R4 comes
// before 10.0R1.  A version that is written another way counts by the
// numbers in it.
func compareVersions(a, b string) int {
	numbers := func(version string) []int {
		parts := number.FindAllString(version, -1)
		if release := releaseParts.FindStringSubmatch(version); release != nil {
			parts = release[1:]
		}
		found := make([]int, len(parts))
		for i, part := range parts {
			found[i], _ = strconv.Atoi(part) // no number counts as none
		}
		return found
	}
	if c := slices.Compare(numbers(a), numbers(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}
