package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlmjohnson/be"
)

// confModule is a configuration module the way Juniper writes one, with
// more statements under system services netconf.
func confModule(name, release, more string) string {
	return fmt.Sprintf(`/*
 * Copyright (c) Juniper Networks, Inc.
 */
 module %s {
   namespace "http://yang.juniper.net/junos/conf/system";
   prefix jc-system;
   import junos-conf-root {
     prefix jc;
   }
   description "Junos system configuration module";
   revision 2025-01-01 {
     description "Junos: %s";
   }
   augment /jc:configuration {
     uses system-group;
   }
   augment /jc:configuration/jc:groups {
     uses system-group;
   }
   grouping system-group {
     container system {
       description "System parameters";
       uses juniper-system;
     }
   }
   grouping juniper-system {
     uses apply-advanced;
     leaf host-name {
       type string;
     }
     container services {
       uses apply-advanced;
       container netconf {
         description "Allow NETCONF connections";
         uses apply-advanced;
         container ssh {
           uses apply-advanced;
           leaf port {
             type uint16;
           }
         }
         leaf rfc-compliant {
           type empty;
         }
         %s
       }
     }
   }
   grouping apply-advanced {
     leaf-list apply-groups {
       type string;
     }
     leaf-list apply-groups-except {
       type string;
     }
   }
 }
`, name, release, more)
}

// The two replies as a release with a model declares them, and as one
// without.
const (
	modelled = `
   rpc get-system-information {
     output {
       container system-information {
         uses system-information-block;
       }
     }
   }
   rpc get-commit-information {
     input {
       leaf detail {
         type empty;
       }
     }
     output {
       container commit-information {
         uses commit-information-block;
       }
     }
   }
   grouping system-information-block {
     leaf host-name { type string; }
     leaf hardware-model { type string; }
     leaf os-name { type string; }
     leaf os-version { type string; }
     leaf serial-number { type string; }
   }
   grouping commit-information-block {
     list commit-history {
       leaf sequence-number { type int32; }
       leaf date-time { type string; }
       leaf user { type string; }
       leaf client { type string; }
       leaf comment { type string; }
       leaf log { type string; }
       leaf commit-confirmed { type string; }
     }
   }`
	notModelled = `
   rpc get-system-information {
     output {
       choice output_c {
         case output-tag {
           leaf output { type string; }
         }
         case actual-tag {
           anyxml system-information;
         }
       }
     }
   }
   rpc get-commit-information {
     output {
       choice output_c {
         case actual-tag {
           anyxml commit-information;
         }
       }
     }
   }`
)

// rpcModule is an rpc module with the requests given.
func rpcModule(name, release, rpcs string) string {
	return fmt.Sprintf(`module %s {
   namespace "http://yang.juniper.net/junos/rpc/system";
   prefix system;
   revision 2025-01-01 {
     description "Junos: %s";
   }%s
 }
`, name, release, rpcs)
}

// write puts files under a new directory and returns it.
func write(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		be.NilErr(t, os.MkdirAll(filepath.Dir(path), 0o750))
		be.NilErr(t, os.WriteFile(path, []byte(content), 0o600))
	}
	return dir
}

const versionOneOne = "leaf version-1.1 { type empty; }"

// Releases are found wherever their modules are and whatever their files
// are called, told apart by family, and put in the order of their versions.
// One that is there twice and says the same both times is one release.
func TestRun(t *testing.T) {
	tls := `container tls {
           uses apply-advanced;
           leaf local-certificate { type string; }
           container traceoptions {
             leaf level { type string; }
           }
         }`
	dir := write(t, map[string]string{
		"9.3/9.3R4/junos/conf/junos-conf-system@2009-01-01.yang":   confModule("junos-conf-system", "9.3R4.4", "leaf old { type empty; }"),
		"9.3/9.3R4/junos/rpc/junos-rpc-system@2009-01-01.yang":     rpcModule("junos-rpc-system", "9.3R4.4", notModelled),
		"24.2/24.2R1.17/conf/models/junos-conf-system.yang":        confModule("junos-conf-system", "24.2R1.17", tls),
		"24.2/24.2R1.17/rpc/models/junos-rpc-system.yang":          rpcModule("junos-rpc-system", "24.2R1.17", modelled),
		"24.4/24.4R1/conf/models/junos-conf-system.yang":           confModule("junos-conf-system", "24.4R1.9", tls),
		"24.4/24.4R1/rpc/models/junos-rpc-system.yang":             rpcModule("junos-rpc-system", "24.4R1.9", modelled),
		"from-a-device/a.yang":                                     confModule("junos-conf-system", "25.4R1.12", tls+versionOneOne),
		"from-a-device/b.yang":                                     rpcModule("junos-rpc-system", "25.4R1.12", modelled),
		"from-another/a.yang":                                      confModule("junos-conf-system", "25.4R1.12", tls+versionOneOne),
		"from-another/b.yang":                                      rpcModule("junos-rpc-system", "25.4R1.12", modelled),
		"25.4/25.4R1-EVO/conf/models/junos-conf-system.yang":       confModule("junos-conf-system", "25.4R1.13-EVO", versionOneOne),
		"25.4/25.4R1-EVO/rpc/models/junos-rpc-system.yang":         rpcModule("junos-rpc-system", "25.4R1.13-EVO", modelled),
		"25.4/25.4R1/junos-ex/conf/junos-ex-conf-system.yang":      confModule("junos-ex-conf-system", "25.4R1.12", ""),
		"25.4/25.4R1/junos-ex/rpc/junos-ex-rpc-system.yang":        rpcModule("junos-ex-rpc-system", "25.4R1.12", modelled),
		"25.4/25.4R1/junos/rpc/junos-rpc-interfaces.yang":          rpcModule("junos-rpc-interfaces", "25.4R1.12", ""),
		"25.4/25.4R1/junos/conf/junos-conf-root.yang":              "module junos-conf-root { container configuration; }",
		"25.4/25.4R1/junos/conf/README.txt":                        "not YANG {",
		"25.4/25.4R1/junos-ex/conf/junos-ex-conf-system.yang.orig": "not YANG {",
	})

	out, err := run([]string{dir})
	be.NilErr(t, err)
	be.Equal(t, `junos: 4 releases, 9.3R4.4 to 25.4R1.12

  system services netconf
    9.3R4.4: 4 statements
    24.2R1.17: 7 statements
      + tls and 3 under it
      - old
    25.4R1.12: 8 statements
      + version-1.1

  get-system-information
    9.3R4.4: has no model of the reply
    24.2R1.17 to 25.4R1.12: declares every element the library reads

  get-commit-information
    9.3R4.4: has no model of the reply
    24.2R1.17 to 25.4R1.12: declares every element the library reads

junos evolved: 25.4R1.13-EVO

  system services netconf
    25.4R1.13-EVO: 4 statements

  get-system-information
    25.4R1.13-EVO: declares every element the library reads

  get-commit-information
    25.4R1.13-EVO: declares every element the library reads

junos-ex: 25.4R1.12

  system services netconf
    25.4R1.12: 3 statements

  get-system-information
    25.4R1.12: declares every element the library reads

  get-commit-information
    25.4R1.12: declares every element the library reads

`, out)
}

// What the library reads and a release does not declare fails the run, and
// so does a set of modules that is not what the command takes it for.
func TestRunProblems(t *testing.T) {
	conf := confModule("junos-conf-system", "26.2R1.7", "")
	rpc := rpcModule("junos-rpc-system", "26.2R1.7", modelled)

	tests := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{
			name: "an element is gone",
			files: map[string]string{
				"conf.yang": conf,
				"rpc.yang":  strings.NewReplacer("leaf serial-number { type string; }", "", "leaf log { type string; }", "", "leaf user { type string; }", "").Replace(rpc),
			},
			want: []string{
				"problem: junos 26.2R1.7: the reply of get-system-information has no serial-number",
				"problem: junos 26.2R1.7: the reply of get-commit-information has no user, log",
				"    26.2R1.7: FAILS: the reply of get-system-information has no serial-number",
			},
		},
		{
			name: "the list of the history is gone",
			files: map[string]string{
				"conf.yang": conf,
				"rpc.yang":  strings.Replace(rpc, "list commit-history", "list history", 1),
			},
			want: []string{"problem: junos 26.2R1.7: the reply of get-commit-information: commit-history is not declared in module junos-rpc-system"},
		},
		{
			name: "a request is gone",
			files: map[string]string{
				"conf.yang": conf,
				"rpc.yang":  strings.Replace(rpc, "rpc get-commit-information", "rpc get-commits", 1),
			},
			want: []string{"problem: junos 26.2R1.7: module junos-rpc-system has no rpc get-commit-information"},
		},
		{
			name: "a reply without an output",
			files: map[string]string{
				"conf.yang": conf,
				"rpc.yang":  rpcModule("junos-rpc-system", "26.2R1.7", "rpc get-system-information { }"+strings.Replace(modelled, "rpc get-system-information", "rpc other", 1)),
			},
			want: []string{"problem: junos 26.2R1.7: rpc get-system-information of module junos-rpc-system has no output"},
		},
		{
			name: "a grouping of another module",
			files: map[string]string{
				"conf.yang": conf,
				"rpc.yang":  strings.Replace(rpc, "uses system-information-block", "uses jt:system-information-block", 1),
			},
			want: []string{"problem: junos 26.2R1.7: module junos-rpc-system has no grouping jt:system-information-block"},
		},
		{
			name: "no NETCONF service in the configuration",
			files: map[string]string{
				"conf.yang": strings.Replace(conf, "container netconf", "container netconf-ng", 1),
				"rpc.yang":  rpc,
			},
			want: []string{"problem: junos 26.2R1.7: module junos-conf-system declares no system services netconf"},
		},
		{
			name:  "half a release",
			files: map[string]string{"conf.yang": conf, "ex.yang": rpcModule("junos-ex-rpc-system", "26.2R1.7", modelled)},
			want: []string{
				"problem: junos 26.2R1.7: no rpc module",
				"problem: junos-ex 26.2R1.7: no configuration module",
				"    26.2R1.7: has no rpc module",
			},
		},
		{
			name: "a release that says two things",
			files: map[string]string{
				"conf.yang": conf, "rpc.yang": rpc,
				"device/conf.yang": confModule("junos-conf-system", "26.2R1.7", versionOneOne),
				"device/rpc.yang":  rpcModule("junos-rpc-system", "26.2R1.7", notModelled),
			},
			want: []string{
				"problem: junos 26.2R1.7: module junos-conf-system in ",
				"device/conf.yang does not say what the one in ",
				"problem: junos 26.2R1.7: module junos-rpc-system in ",
			},
		},
		{
			name:  "a module that names no release",
			files: map[string]string{"conf.yang": strings.Replace(conf, "Junos: ", "", 1), "rpc.yang": rpc},
			want:  []string{"conf.yang: module junos-conf-system does not say which release of Junos it is of"},
		},
		{
			name:  "a file that is not YANG",
			files: map[string]string{"conf.yang": conf, "rpc.yang": rpc, "broken.yang": "module broken {\n leaf a"},
			want:  []string{"broken.yang: line 2: the file ends inside a statement"},
		},
		{
			name:  "no modules",
			files: map[string]string{"other.yang": "module other { }"},
			want:  []string{"problem: no junos-conf-system or junos-rpc-system module under "},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := run([]string{write(t, tt.files)})
			be.Nonzero(t, err)
			be.Equal(t, fmt.Sprintf("problems: %d", strings.Count(out, "problem: ")), err.Error())
			for _, want := range tt.want {
				be.In(t, want, out)
			}
		})
	}

	_, err := run([]string{filepath.Join(t.TempDir(), "nothing")})
	be.Nonzero(t, err)
}

func TestCompareVersions(t *testing.T) {
	ordered := []string{
		"9.3R4.4", "17.4R1.16", "20.4X75-D10", "24.2R1.17", "24.4R1.9", "24.4R2.3",
		"25.4R1.12", "25.4R1.12-EVO", "25.4R1-S1.7", "25.4R1-S1.7-EVO", "25.4R1-S2.3", "25.4R2.5", "26.2R1.7",
	}
	for i, a := range ordered {
		for j, b := range ordered {
			be.Equal(t, min(max(i-j, -1), 1), compareVersions(a, b))
		}
	}
}
