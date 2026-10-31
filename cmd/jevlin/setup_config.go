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
// under a name of its own and a fresh format-1 file takes its place, written
// with the directories and endpoints the earlier file named (earlierConfig),
// because the agent record is in that state directory and the key is bound
// to those endpoints; the key and the files kept with it, which 0.6.x kept
// beside its intake directory, move beside the state directory where the
// two differ (identityMoves); any
// other file, a format-1 file with a mistake in it included, is refused with
// the error and its path, and nothing is written.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
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
	// stateDir and sessionsDir are written as given; empty is the default
	// layout under home.
	stateDir    string
	sessionsDir string
	// fromEnv names the endpoints a JEVLIN_* variable chose for this run,
	// which a value carried from an earlier file does not replace.
	fromEnv map[string]bool
}

func resolveSetupValues(home string, getenv func(string) string) setupValues {
	v := setupValues{home: home, fromEnv: map[string]bool{}}
	pick := func(name, def string) string {
		if e := getenv(name); e != "" {
			v.fromEnv[name] = true
			return e
		}
		return def
	}
	v.router = pick(config.EnvRouterURL, config.DefaultRouterURL)
	v.platformURL = pick(config.EnvPlatformURL, config.DefaultPlatformBaseURL)
	v.agentsAPIURL = pick(config.EnvAgentsAPIURL, config.DefaultAgentsAPIURL)
	return v
}

// earlierConfig is what a config an earlier version wrote says about where
// this installation keeps its identity and which endpoints it talks to:
// the [mining] state directory (the registration, the agent record and
// credentials.json sit beside it), the [miner] sessions directory, the
// router ([miner] router_url, or the one search-router [[provider]]
// upstream setup wrote), and the [platform] portal and agents API. The key
// connect stored is bound to that router and agents API, so a fresh file
// that named the compiled defaults instead would leave the key refused for
// every search, and one that named the default state directory would make
// connect register a second agent beside the first. Nothing else is
// carried: a setting changed by hand is copied by hand, under its format-1
// key. A file that does not decode carries nothing.
type earlierConfig struct {
	stateDir, sessionsDir             string
	router, platformURL, agentsAPIURL string
	// intakeDir is [miner] intake_dir as written. 0.6.x kept
	// credentials.json, the registration journal, claim.json and the
	// release check's record beside it (its minerRoot), not beside the
	// state directory: identityRoot.
	intakeDir string
}

// identityRoot is the directory an earlier version kept the files beside
// credentials.json in: the parent of [miner] intake_dir, else of the state
// directory, which is where that version defaulted the intake directory;
// "" when the file names neither, or names it relative, which setup would
// read against whatever directory it was run from (refusedConfigUpdate
// skips one for the same reason).
func (e earlierConfig) identityRoot() string {
	for _, dir := range []string{e.intakeDir, e.stateDir} {
		if dir != "" {
			if !filepath.IsAbs(dir) {
				return ""
			}
			return filepath.Dir(filepath.Clean(dir))
		}
	}
	return ""
}

func readEarlierConfig(data []byte) earlierConfig {
	var f struct {
		Provider []struct {
			Name     string `toml:"name"`
			Upstream string `toml:"upstream"`
		} `toml:"provider"`
		Mining struct {
			StateDir string `toml:"state_dir"`
		} `toml:"mining"`
		Miner struct {
			RouterURL   string `toml:"router_url"`
			SessionsDir string `toml:"sessions_dir"`
			IntakeDir   string `toml:"intake_dir"`
		} `toml:"miner"`
		Platform struct {
			BaseURL      string `toml:"base_url"`
			AgentsAPIURL string `toml:"agents_api_url"`
		} `toml:"platform"`
	}
	if _, err := toml.Decode(string(data), &f); err != nil {
		return earlierConfig{}
	}
	e := earlierConfig{
		stateDir:     f.Mining.StateDir,
		sessionsDir:  f.Miner.SessionsDir,
		router:       f.Miner.RouterURL,
		platformURL:  f.Platform.BaseURL,
		agentsAPIURL: f.Platform.AgentsAPIURL,
		intakeDir:    f.Miner.IntakeDir,
	}
	// The provider upstream was the router only in the shape setup wrote;
	// an upstream an earlier proxy configuration named is not a router.
	if e.router == "" && len(f.Provider) == 1 && f.Provider[0].Name == "search-router" {
		e.router = f.Provider[0].Upstream
	}
	// The portal's retired origin is carried as its successor. 0.6.x matched
	// a claim link on the successor for a config naming the retired portal
	// (its one compiled alias); format 1 has no alias, so carried as written
	// it would refuse every claim link the platform now issues.
	if o, err := urlOrigin(e.platformURL); err == nil && o == retiredPortal {
		e.platformURL = config.DefaultPlatformBaseURL
	}
	// An earlier version kept the sessions directory beside the state
	// directory unless told otherwise, as format 1 does.
	if e.sessionsDir == "" && e.stateDir != "" {
		e.sessionsDir = filepath.Join(filepath.Dir(e.stateDir), "sessions")
	}
	return e
}

// retiredPortal is the portal origin the platform moved from, whose
// successor is config.DefaultPlatformBaseURL.
const retiredPortal = "https://platform.nyks.dev"

// carrying is v with what an earlier file named in place of a default; an
// endpoint a variable chose for this run stays the variable's.
func (v setupValues) carrying(e earlierConfig) setupValues {
	for _, f := range []struct {
		env   string
		value string
		field *string
	}{
		{config.EnvRouterURL, e.router, &v.router},
		{config.EnvPlatformURL, e.platformURL, &v.platformURL},
		{config.EnvAgentsAPIURL, e.agentsAPIURL, &v.agentsAPIURL},
	} {
		if f.value != "" && !v.fromEnv[f.env] {
			*f.field = f.value
		}
	}
	// A directory is carried only when it is absolute: a relative one would
	// be read against whatever directory each command runs from, and the
	// installation's own default is where an earlier version kept it as
	// well when setup wrote it.
	abs := func(dir string) string {
		if filepath.IsAbs(dir) {
			return dir
		}
		return ""
	}
	if state := abs(e.stateDir); state != "" {
		v.stateDir, v.sessionsDir = state, abs(e.sessionsDir)
	} else if sessions := abs(e.sessionsDir); sessions != "" {
		v.sessionsDir = sessions
	}
	return v
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
	stateDir, sessionsDir := v.stateDir, v.sessionsDir
	if stateDir == "" {
		stateDir = filepath.Join(v.home, "state")
	}
	if sessionsDir == "" {
		sessionsDir = filepath.Join(v.home, "sessions")
	}
	dirs, err := quoteAll(
		"state_dir", stateDir,
		"sessions_dir", sessionsDir,
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
	// identityFrom and identityTo are where an earlier version kept the
	// files beside credentials.json and where this one reads them (the
	// parent of the state directory the new file names), when the two
	// differ (configReplaced); identityMoves.
	identityFrom, identityTo string
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
func planSetupConfig(path string, existing []byte, v setupValues, validate func(data []byte, source string) error, exists func(string) bool) (configPlan, error) {
	fresh := func() ([]byte, error) {
		data, err := renderFreshConfig(v)
		if err != nil {
			return nil, err
		}
		if err := validate(data, path); err != nil {
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
	err := validate(existing, path)
	switch {
	case err == nil:
		return configPlan{outcome: configLeft}, nil
	case errors.Is(err, config.ErrLegacyConfig):
		earlier := readEarlierConfig(existing)
		v = v.carrying(earlier)
		data, ferr := fresh()
		if ferr != nil {
			return configPlan{}, ferr
		}
		aside, aerr := asideName(path, exists)
		if aerr != nil {
			return configPlan{}, aerr
		}
		plan := configPlan{outcome: configReplaced, data: data, aside: aside}
		if from := earlier.identityRoot(); from != "" {
			if cfg, err := config.LoadBytes(data, func(string) string { return "" }); err == nil {
				if to := installRoot(cfg.Search); filepath.IsAbs(to) && !sameDirectory(from, to) {
					plan.identityFrom, plan.identityTo = from, to
				}
			}
		}
		return plan, nil
	default:
		// A parse or schema error names the file already (validate's source
		// is path); a value's rule names only its key, and gets the path.
		if !strings.Contains(err.Error(), path) {
			return configPlan{}, fmt.Errorf("%w: %s: %v", errConfigInvalid, path, err)
		}
		return configPlan{}, fmt.Errorf("%w: %v", errConfigInvalid, err)
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
// will, with no environment, so only the file's own bytes are judged; an
// error names the file as source.
func validateConfigBytes(data []byte, source string) error {
	_, err := config.LoadBytesAs(data, source, func(string) string { return "" })
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

// identityFiles are the files an installation keeps beside credentials.json,
// outside every directory a sandbox may write: what an earlier version kept
// beside its intake directory and this one keeps beside its state
// directory.
var identityFiles = []string{credentialsFile, registrationJournalFile, claimRecordFile, updateStampFile}

// identityMoves is each identity file present in from, with the name it
// takes in to. A file already at a destination is a conflict, and so is a
// destination directory that does not exist: both stop setup before
// anything is written, with the exact moves to make by hand. The one
// exception is the release check's record, which says only when the last
// check ran: the earlier version's replaces one already there.
func identityMoves(from, to string) (moves [][2]string, conflict error) {
	if from == "" || to == "" {
		return nil, nil
	}
	for _, name := range identityFiles {
		src := filepath.Join(from, name)
		if !lexists(src) {
			continue
		}
		dst := filepath.Join(to, name)
		if lexists(dst) && name != updateStampFile {
			return nil, fmt.Errorf("%s is already there, so the earlier version's %s cannot take its place", dst, src)
		}
		moves = append(moves, [2]string{src, dst})
	}
	if len(moves) > 0 && !lexists(to) {
		return nil, fmt.Errorf("%s, beside the state directory the new config names, does not exist", to)
	}
	return moves, nil
}

// sameDirectory reports whether a and b name one directory, through a link
// or in another case on a filesystem that ignores it, as well as by text.
func sameDirectory(a, b string) bool {
	if samePath(a, b) {
		return true
	}
	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(ia, ib)
}

// moveIdentity moves each file of moves into place, a variable so a test
// can fail one. A move that fails puts back the ones made before it, so the
// files are all in one place or the other, and reports the failure.
var moveIdentity = func(moves [][2]string) error {
	for i, m := range moves {
		if err := moveIdentityFile(m[0], m[1]); err != nil {
			for j := i - 1; j >= 0; j-- {
				_ = moveIdentityFile(moves[j][1], moves[j][0])
			}
			return err
		}
	}
	return nil
}

// undoIdentity puts moves back, for a run that fails after moving them.
func undoIdentity(moves [][2]string) {
	for j := len(moves) - 1; j >= 0; j-- {
		_ = moveIdentityFile(moves[j][1], moves[j][0])
	}
}

// identityFileMaxBytes bounds the read of an identity file copied across
// filesystems: credentials.json, the journal, the claim record and the
// release check's record are each a few hundred bytes.
const identityFileMaxBytes = 1 << 20

// moveIdentityFile moves one file durably. A rename cannot cross
// filesystems, and a state directory on another disk than the earlier
// installation's intake directory is one setup must handle, so there the
// file is copied owner-only and synced, published by rename in the
// destination's directory, and only then removed from the source.
func moveIdentityFile(src, dst string) error {
	// The release check's record is the one file a move replaces
	// (identityMoves); a move does not replace a file on Windows, so the
	// one there goes first.
	if filepath.Base(dst) == updateStampFile && lexists(dst) && lexists(src) {
		if err := fsx.RemoveFileDurable(dst); err != nil {
			return err
		}
	}
	err := renameIdentityFile(src, dst)
	var stage *fsx.StageError
	switch {
	case err == nil, errors.As(err, &stage) && stage.Published:
		return nil
	case !crossDevice(err):
		return err
	}
	data, _, rerr := fsx.ReadRegularNoFollow(src, identityFileMaxBytes)
	if rerr != nil {
		return rerr
	}
	if werr := fsx.WriteFileAtomic(filepath.Dir(dst), filepath.Base(dst), data, 0o600); werr != nil {
		return werr
	}
	return fsx.RemoveFileDurable(src)
}

// renameIdentityFile is the durable rename moveIdentityFile tries first, a
// variable so a test can refuse it the way another filesystem does.
var renameIdentityFile = fsx.MoveFileDurable

// crossDevice reports whether err is a rename refused because the two
// names are on different filesystems: EXDEV, or Windows'
// ERROR_NOT_SAME_DEVICE (17, which is EEXIST elsewhere).
func crossDevice(err error) bool {
	return errors.Is(err, syscall.EXDEV) || (runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(17)))
}

// relativeEarlierDirs reports whether an earlier config names its intake or
// state directory by a relative path, for which setup plans no move.
func relativeEarlierDirs(data []byte) bool {
	e := readEarlierConfig(data)
	for _, dir := range []string{e.intakeDir, e.stateDir} {
		if dir != "" && !filepath.IsAbs(dir) {
			return true
		}
	}
	return false
}
