package main

import (
	"strings"
	"testing"
)

func TestPluginManifestVersionsReadBothShapes(t *testing.T) {
	v, err := PluginManifestVersions("plugin.json", []byte(`{"name":"jevlin","version":"0.4.3","keywords":["x"]}`))
	if err != nil || strings.Join(v, ",") != "0.4.3" {
		t.Fatalf("plugin manifest: %v %v", v, err)
	}
	v, err = PluginManifestVersions(".claude-plugin/marketplace.json", []byte(`{"name":"jevlin","plugins":[{"name":"jevlin","source":"./","version":"0.4.3"}]}`))
	if err != nil || strings.Join(v, ",") != "0.4.3" {
		t.Fatalf("marketplace manifest: %v %v", v, err)
	}
	for _, bad := range []string{`{"name":"jevlin"}`, `{"plugins":[{"name":"jevlin"}]}`, `not json`} {
		if _, err := PluginManifestVersions("plugin.json", []byte(bad)); err == nil {
			t.Errorf("%s: want an error", bad)
		}
	}
}

func TestCheckPluginManifestVersionRefusesAStaleOne(t *testing.T) {
	v := ReleaseVersion{Major: 0, Minor: 4, Patch: 4}
	if err := CheckPluginManifestVersion("plugin.json", []byte(`{"version":"0.4.4"}`), v); err != nil {
		t.Fatal(err)
	}
	err := CheckPluginManifestVersion(".claude-plugin/marketplace.json", []byte(`{"plugins":[{"name":"jevlin","version":"0.4.3"}]}`), v)
	if err == nil || !strings.Contains(err.Error(), "0.4.3") || !strings.Contains(err.Error(), "marketplace.json") {
		t.Fatalf("a stale marketplace entry was accepted, or the refusal does not say which: %v", err)
	}
}
