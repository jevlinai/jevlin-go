package main

// Decoded-document arithmetic for Codex's config.toml. Every edit this
// client makes to that file — writing the region, moving a table out of it,
// adding a marked line to a participant's profile, taking any of them back —
// is checked the same way before a byte is written: the file after the edit
// must DECODE to the file before it plus exactly the keys the edit means to
// add, or minus exactly the keys it means to take away. The line scan that
// finds headers and keys is deliberately not trusted (see tomlHeaderLine);
// this is the net under it, and a miss in the scan can only ever cost a
// refusal, never a participant's key.

import (
	"reflect"
	"sort"

	"github.com/BurntSushi/toml"
)

type tomlDoc = map[string]any

func decodeTOMLDoc(s string) (tomlDoc, bool) {
	var doc tomlDoc
	if _, err := toml.Decode(s, &doc); err != nil {
		return nil, false
	}
	if doc == nil {
		doc = tomlDoc{}
	}
	return doc, true
}

// deepMergeTOML writes src into dst: tables merge, anything else in src
// replaces what dst had.
func deepMergeTOML(dst, src tomlDoc) {
	for k, v := range src {
		sv, srcTable := v.(map[string]any)
		dv, dstTable := dst[k].(map[string]any)
		if srcTable && dstTable {
			deepMergeTOML(dv, sv)
			continue
		}
		dst[k] = v
	}
}

// deleteTOMLPath removes the key at path and every table it leaves empty
// above it. A path that is not there is nothing to delete.
func deleteTOMLPath(doc tomlDoc, path ...string) {
	if len(path) == 0 {
		return
	}
	if len(path) == 1 {
		delete(doc, path[0])
		return
	}
	child, ok := doc[path[0]].(map[string]any)
	if !ok {
		return
	}
	deleteTOMLPath(child, path[1:]...)
	if len(child) == 0 {
		delete(doc, path[0])
	}
}

// lookupTOMLPath reads the value at path.
func lookupTOMLPath(doc tomlDoc, path ...string) (any, bool) {
	var cur any = doc
	for _, k := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[k]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// copyTOMLDoc is a deep copy, so an expected document can be built from a
// decoded one without touching it.
func copyTOMLDoc(doc tomlDoc) tomlDoc {
	out := make(tomlDoc, len(doc))
	for k, v := range doc {
		if m, ok := v.(map[string]any); ok {
			out[k] = copyTOMLDoc(m)
			continue
		}
		out[k] = v
	}
	return out
}

func tomlDocsEqual(a, b tomlDoc) bool { return reflect.DeepEqual(a, b) }

// codexOurPaths are every key this client's region can define, the legacy
// table included: a table inside the markers that defines one of these is
// not somebody else's to keep, and the region is refused rather than read.
func codexOurPaths() [][]string {
	return [][]string{
		{"default_permissions"},
		{"features", "network_proxy"},
		{"permissions", codexProfileName},
		{codexSandboxTable},
	}
}

// sortedKeys is a document's top-level keys, sorted, for a sentence.
func sortedKeys(doc tomlDoc) []string {
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortStrings(s []string) { sort.Strings(s) }

// keepDeclaredTables puts back, empty, every table decl declares that out
// lacks. A key of another kind where decl has a table is left alone, for
// the comparison to catch.
func keepDeclaredTables(out, decl tomlDoc) {
	for k, v := range decl {
		child, ok := v.(map[string]any)
		if !ok {
			continue
		}
		have, ok := out[k].(map[string]any)
		if !ok {
			if _, present := out[k]; present {
				continue
			}
			have = map[string]any{}
			out[k] = have
		}
		keepDeclaredTables(have, child)
	}
}

// subtractDoc is doc without every leaf that ours defines, and without a
// table that taking them out left empty.
func subtractDoc(doc, ours tomlDoc) tomlDoc {
	out := copyTOMLDoc(doc)
	var walk func(prefix []string, t tomlDoc)
	walk = func(prefix []string, t tomlDoc) {
		for k, v := range t {
			path := append(append([]string{}, prefix...), k)
			if child, ok := v.(map[string]any); ok && len(child) > 0 {
				walk(path, child)
				continue
			}
			deleteTOMLPath(out, path...)
		}
	}
	walk(nil, ours)
	return out
}
