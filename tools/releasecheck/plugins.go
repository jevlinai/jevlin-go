package main

import (
	"encoding/json"
	"fmt"
)

// pluginManifests are the marketplace manifests that carry a version of
// their own, and have to carry the release's. Claude Code keeps a
// plugin at the version its manifest names: a tag that bumps
// npm/package.json and leaves these behind ships a client that updates
// and a marketplace skill that does not (#134 review).
var pluginManifests = []string{
	"plugin/.claude-plugin/plugin.json",
	".claude-plugin/marketplace.json",
	"plugin/plugin.json",
}

// PluginManifestVersions reads every "version" a manifest declares: the
// top-level one of a plugin manifest, and each entry's in a marketplace
// manifest's "plugins" list. Decoded permissively for the same reason
// npm/package.json is — the manifests carry fields the release has no
// opinion on.
func PluginManifestVersions(path string, manifest []byte) ([]string, error) {
	var m struct {
		Version *string `json:"version"`
		Plugins []struct {
			Name    string  `json:"name"`
			Version *string `json:"version"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	var out []string
	if m.Version != nil {
		out = append(out, *m.Version)
	}
	for _, p := range m.Plugins {
		if p.Version == nil {
			return nil, fmt.Errorf("%s: plugin %q has no %q field", path, p.Name, "version")
		}
		out = append(out, *p.Version)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s has no %q field", path, "version")
	}
	return out, nil
}

// CheckPluginManifestVersion is CheckNPMPackageVersion for one plugin
// manifest: every version it declares is the tag's.
func CheckPluginManifestVersion(path string, manifest []byte, v ReleaseVersion) error {
	got, err := PluginManifestVersions(path, manifest)
	if err != nil {
		return err
	}
	for _, g := range got {
		if g == "" {
			return fmt.Errorf("%s has an empty %q field", path, "version")
		}
		if g != v.String() {
			return fmt.Errorf("%s at the tag says version %q, but the tag is %s (want %q): "+
				"the release PR bumps the plugin manifests with npm/package.json",
				path, g, v.Tag(), v.String())
		}
	}
	return nil
}
