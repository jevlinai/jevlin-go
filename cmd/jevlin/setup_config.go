package main

// setup's config: one fresh file for a new installation, and what is done
// with a file that is already there.
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
// An existing file is never rewritten. It is loaded first, always, through
// pkg/config (planSetupConfig): a format-1 file that loads is left byte for
// byte; a file with no schema line, from an earlier version, is set aside
// under a name of its own and a fresh format-1 file takes its place; any
// other file, a format-1 file with a mistake in it included, is refused with
// the error and its path, and nothing is written.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

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

// renderFreshConfig is a fresh installation's config in configuration
// format 1, with every value quoted: the schema line, one section per
// endpoint, and [search] with the state and sessions directories under the
// installation.
func renderFreshConfig(v setupValues) ([]byte, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "schema = %d\n", config.Schema)
	for _, section := range []struct{ name, url string }{
		{"router", v.router},
		{"platform", v.platformURL},
		{"agents", v.agentsAPIURL},
	} {
		lines, err := quoteAll("url", section.url)
		if err != nil {
			return nil, fmt.Errorf("[%s] %w", section.name, err)
		}
		b.WriteString("\n[" + section.name + "]\n")
		writeTOMLLines(&b, lines)
	}
	// Every byte this client generates into a config is ASCII
	// (asciiGeneratedConfig guards it): Windows PowerShell 5.1 reads a file
	// with no byte-order mark in the system's ANSI code page, so a comment
	// with an em dash once showed the participant `\u00e2\u20ac\u201d`
	// where the dash should be.
	dirs, err := quoteAll(
		"state_dir", filepath.Join(v.home, "state"),
		"sessions_dir", filepath.Join(v.home, "sessions"),
	)
	if err != nil {
		return nil, err
	}
	b.WriteString("\n[search]\n")
	writeTOMLLines(&b, dirs)
	return []byte(b.String()), nil
}

// configOutcome is what setup decided for one file.
type configOutcome int

const (
	configFresh    configOutcome = iota // no file: a new one is written
	configLeft                          // format 1 and valid: untouched
	configReplaced                      // an older format: set aside, and a new one written
)

// configPlan is the decision before anything is written.
type configPlan struct {
	outcome configOutcome
	data    []byte // the bytes to publish (fresh or replaced); nil when left
	aside   string // where an older file is moved (configReplaced)
}

// errConfigInvalid marks a refusal of the existing file, as opposed to a
// failure to read it.
var errConfigInvalid = errors.New("config is not valid")

// planSetupConfig decides what setup does with the config at path. existing
// is the file's current bytes, or nil when there is none. A valid format-1
// file is left byte for byte. An older file is never reinterpreted: it is set
// aside under a name of its own, so nothing the participant wrote is lost,
// and a fresh format-1 file takes its place. A format-1 file that does not
// load is refused, because it is the participant's current configuration and
// setup does not guess what it should have said.
func planSetupConfig(path string, existing []byte, v setupValues, validate func([]byte) error, exists func(string) bool) (configPlan, error) {
	fresh := func() ([]byte, error) {
		data, err := renderFreshConfig(v)
		if err != nil {
			return nil, err
		}
		if err := validate(data); err != nil {
			return nil, fmt.Errorf("the config setup would write does not load (%w); nothing was written", err)
		}
		return data, nil
	}
	if existing == nil {
		data, err := fresh()
		if err != nil {
			return configPlan{}, err
		}
		return configPlan{outcome: configFresh, data: data}, nil
	}
	err := validate(existing)
	switch {
	case err == nil:
		return configPlan{outcome: configLeft}, nil
	case errors.Is(err, config.ErrLegacyConfig):
		data, ferr := fresh()
		if ferr != nil {
			return configPlan{}, ferr
		}
		aside, aerr := asideName(path, exists)
		if aerr != nil {
			return configPlan{}, aerr
		}
		return configPlan{outcome: configReplaced, data: data, aside: aside}, nil
	default:
		return configPlan{}, fmt.Errorf("%w: %s: %v", errConfigInvalid, path, err)
	}
}

// asideName is the first of path.old, path.old.1, ... that does not exist.
func asideName(path string, exists func(string) bool) (string, error) {
	for i := 0; i < 100; i++ {
		name := path + ".old"
		if i > 0 {
			name += "." + strconv.Itoa(i)
		}
		if !exists(name) {
			return name, nil
		}
	}
	return "", fmt.Errorf("%s: no free name to set the older configuration aside under; move %s.old* away", path, path)
}

// validateConfigBytes loads data through pkg/config exactly as every command
// will, with no environment, so only the file's own bytes are judged.
func validateConfigBytes(data []byte) error {
	_, err := config.LoadBytes(data, func(string) string { return "" })
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
