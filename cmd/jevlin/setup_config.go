package main

// setup's config: one fresh file for a new installation, and a migration
// policy for a file that is already there.
//
// Nothing here interpolates a path or a value into TOML text. Every string
// goes through tomlString, which produces a valid TOML basic string whatever
// the input holds — a participant's home directory can contain a quote, a
// backslash (every Windows path does) or a control character, and the shell
// script this replaces wrote `state_dir = "$HOME_DIR/state"` and produced an
// invalid file for each of those. The exact bytes are then loaded through
// pkg/config before anything is published, so a file setup writes is one the
// runtime has already accepted.
//
// An existing file is never rewritten. It is parsed first, always: a file
// that does not parse is refused with the parse error and its path, whatever
// its text happens to contain (a `[search]` line in a file that does not parse
// proves nothing). A valid file with a [search] table is left byte for byte.
// A valid file without one — a tokendrop-proxy config, most likely — gains
// only the tables it lacks, found from the TOML metadata rather than by
// string matching, and the result is parsed again before it is published.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/BurntSushi/toml"

	"github.com/jevlinai/jevlin-go/pkg/config"
	"github.com/jevlinai/jevlin-go/pkg/fsx"
)

const setupConfigFile = "jevlin.toml"

// tomlString renders s as a TOML basic string. It is the one place setup
// turns a value into TOML text. TOML documents are UTF-8, so a value that is
// not valid UTF-8 has no correct rendering and is an error, not a guess.
func tomlString(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", fmt.Errorf("%q is not valid UTF-8 and cannot be written to a TOML config", s)
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String(), nil
}

// setupValues is everything the config is written from: pkg/config's
// defaults, overridden by the JEVLIN_* setup-time overrides.
type setupValues struct {
	home         string
	router       string
	platformURL  string
	agentsAPIURL string
}

func resolveSetupValues(home string, getenv func(string) string) setupValues {
	pick := func(name, def string) string {
		if v := getenv(name); v != "" {
			return v
		}
		return def
	}
	return setupValues{
		home:         home,
		router:       pick("JEVLIN_ROUTER_URL", config.DefaultRouterURL),
		platformURL:  pick("JEVLIN_PLATFORM_URL", config.DefaultPlatformBaseURL),
		agentsAPIURL: pick("JEVLIN_AGENTS_API_URL", config.DefaultAgentsAPIURL),
	}
}

// tomlLines renders `key = value` pairs with the values quoted and the
// equals signs aligned.
type tomlLine struct {
	key   string
	value string // already TOML
}

func quoteAll(pairs ...string) ([]tomlLine, error) {
	out := make([]tomlLine, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		q, err := tomlString(pairs[i+1])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", pairs[i], err)
		}
		out = append(out, tomlLine{pairs[i], q})
	}
	return out, nil
}

func writeTOMLLines(b *strings.Builder, lines []tomlLine) {
	width := 0
	for _, l := range lines {
		width = max(width, len(l.key))
	}
	for _, l := range lines {
		fmt.Fprintf(b, "%-*s = %s\n", width, l.key, l.value)
	}
}

func platformTable(v setupValues) (string, error) {
	lines, err := quoteAll("base_url", v.platformURL, "agents_api_url", v.agentsAPIURL)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("[platform]\n")
	writeTOMLLines(&b, lines)
	return b.String(), nil
}

// searchTable is the [search] block a fresh config gets: the state directory
// and the sessions directory under the installation. The router comes from
// the [[provider]] upstream above it, as it always has.
func searchTable(v setupValues) (string, error) {
	lines, err := quoteAll(
		"state_dir", filepath.Join(v.home, "state"),
		"sessions_dir", filepath.Join(v.home, "sessions"),
	)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("[search]\n")
	writeTOMLLines(&b, lines)
	return b.String(), nil
}

// renderFreshConfig is a fresh installation's config, with every value quoted.
func renderFreshConfig(v setupValues) ([]byte, error) {
	var b strings.Builder
	provider, err := quoteAll("name", "search-router", "upstream", v.router)
	if err != nil {
		return nil, err
	}
	b.WriteString("[[provider]]\n")
	writeTOMLLines(&b, provider)
	b.WriteString("\n")

	platform, err := platformTable(v)
	if err != nil {
		return nil, err
	}
	b.WriteString(platform)
	b.WriteString("\n")

	// Every byte this client generates into a config is ASCII
	// (asciiGeneratedConfig guards it): Windows PowerShell 5.1 reads a file
	// with no byte-order mark in the system's ANSI code page, so a comment
	// with an em dash once showed the participant `\u00e2\u20ac\u201d`
	// where the dash should be.
	search, err := searchTable(v)
	if err != nil {
		return nil, err
	}
	b.WriteString(search)
	return []byte(b.String()), nil
}

// configOutcome is what the migration policy decided for one file.
type configOutcome int

const (
	configFresh    configOutcome = iota // no file: a new one is written
	configLeft                          // valid, with [search]: untouched
	configMigrated                      // valid, without [search]: missing tables appended
)

// configPlan is the policy's answer before anything is written.
type configPlan struct {
	outcome configOutcome
	data    []byte   // the bytes to publish (fresh or migrated); nil when left
	added   []string // table names appended, for the narration
}

// configTablesDefined reads the TOML metadata for the two tables setup owns.
// Structural, never a text search: `[search]` inside a comment or a string is
// not a table, and a dotted `search.state_dir = "..."` at the root is.
func configTablesDefined(data []byte) (platform, search bool, err error) {
	var doc map[string]any
	md, err := toml.Decode(string(data), &doc)
	if err != nil {
		return false, false, err
	}
	return md.IsDefined("platform"), md.IsDefined("search"), nil
}

// errConfigInvalid marks a refusal of the existing file, as opposed to a
// failure to read it.
var errConfigInvalid = errors.New("config is not valid")

// planSetupConfig applies the migration policy to path. existing is the
// file's current bytes, or nil when there is none.
func planSetupConfig(path string, existing []byte, v setupValues, validate func([]byte) error) (configPlan, error) {
	if existing == nil {
		data, err := renderFreshConfig(v)
		if err != nil {
			return configPlan{}, err
		}
		if err := validate(data); err != nil {
			return configPlan{}, fmt.Errorf("the config setup would write does not load (%w); nothing was written", err)
		}
		return configPlan{outcome: configFresh, data: data}, nil
	}

	// Parse first, always, and whatever the text contains.
	if err := validate(existing); err != nil {
		return configPlan{}, fmt.Errorf("%w: %s: %v", errConfigInvalid, path, err)
	}
	hasPlatform, hasSearch, err := configTablesDefined(existing)
	if err != nil {
		return configPlan{}, fmt.Errorf("%w: %s: %v", errConfigInvalid, path, err)
	}
	if hasSearch {
		return configPlan{outcome: configLeft}, nil
	}

	var tail strings.Builder
	var added []string
	if !hasPlatform {
		t, err := platformTable(v)
		if err != nil {
			return configPlan{}, err
		}
		tail.WriteString("\n" + t)
		added = append(added, "[platform]")
	}
	t, err := searchTable(v)
	if err != nil {
		return configPlan{}, err
	}
	tail.WriteString("\n" + t)
	added = append(added, "[search]")

	next := append([]byte(nil), existing...)
	if len(next) > 0 && next[len(next)-1] != '\n' {
		next = append(next, '\n')
	}
	next = append(next, "\n# Added by jevlin setup.\n"...)
	next = append(next, strings.TrimPrefix(tail.String(), "\n")...)
	if err := validate(next); err != nil {
		return configPlan{}, fmt.Errorf("%s would not load after adding %s (%v); it was left as it is — add them by hand",
			path, strings.Join(added, " and "), err)
	}
	return configPlan{outcome: configMigrated, data: next, added: added}, nil
}

// validateConfigFile loads data through pkg/config exactly as every command
// will: it is written to an owner-only temporary file in dir, loaded with no
// environment (so only the file's own bytes are judged), and removed.
func validateConfigFile(dir string) func([]byte) error {
	return func(data []byte) error {
		f, err := os.CreateTemp(dir, ".jevlin-validate-*.toml")
		if err != nil {
			return err
		}
		name := f.Name()
		defer func() { _ = os.Remove(name) }()
		if err := f.Chmod(0o600); err != nil {
			_ = f.Close()
			return err
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		_, _, err = config.Load([]string{"-config", name}, func(string) string { return "" })
		return err
	}
}

// validateConfigSyntax is the dry run's check: the TOML must decode, but
// nothing is written anywhere to load it through pkg/config.
func validateConfigSyntax(data []byte) error {
	_, _, err := configTablesDefined(data)
	return err
}

// readExistingConfig returns nil, nil when there is no config. A config that
// is a symlink or not a regular file is refused: setup publishes by atomic
// replacement, which would silently turn a link into a file.
func readExistingConfig(path string) ([]byte, error) {
	// Both reads below take the installation's own config path, built from
	// the home directory and a fixed name (setup.go); gosec's taint pass
	// (G703) flags them once any env-derived path elsewhere in the package
	// reaches filepath.Join, which the search tier's lookup does.
	info, err := os.Lstat(path) // #nosec G703 -- the installation's own config path
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", errConfigInvalid, path)
	}
	data, err := os.ReadFile(path) // #nosec G304 G703 -- the installation's own config path
	if err != nil {
		return nil, err
	}
	if data == nil {
		data = []byte{}
	}
	return data, nil
}

func publishSetupConfig(path string, data []byte) error {
	return fsx.WriteFileAtomic(filepath.Dir(path), filepath.Base(path), data, 0o600)
}
