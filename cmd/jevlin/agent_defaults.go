package main

import (
	"context"
	"regexp"
	"strings"
	"time"
)

// What a `search --stdin` request gets when its caller sized nothing. The
// envelope is what the model reads, so a default is a choice about how much of
// it the model pays for: an agent search stays under about 20,000 characters
// (TestAgentSearchOutputStaysBounded).
const (
	// defaultPageChars caps each page's excerpt. max_chars is one budget for
	// the whole list, spent in list order, so later pages would get nothing;
	// a per-page cap gives every page the same share.
	defaultPageChars = 500
	// defaultRewordedLimit is the window a request with search_queries gets:
	// every wording adds pages, and the router's default of 12 would cut
	// them off.
	defaultRewordedLimit = 20
)

// agentOutputDefaults fills in what the request did not size. A caller that
// sent max_chars chose its own budget, and the router takes one excerpt
// control at a time, so the per-page default stays out of its way.
func agentOutputDefaults(mergedLimit, maxChars, pageChars *int, searchQueries []string) (*int, *int) {
	if pageChars == nil && maxChars == nil {
		n := defaultPageChars
		pageChars = &n
	}
	if mergedLimit == nil && len(searchQueries) > 0 {
		n := defaultRewordedLimit
		mergedLimit = &n
	}
	return mergedLimit, pageChars
}

// fastSetFile is the user's fast preset, beside the tier preference.
const fastSetFile = "search-fast-set"

// fastSetNameRE is a preset name as the router writes one. A name that does
// not look like one is never read from the file or sent: the router owns the
// list, and a planted line is not forwarded.
var fastSetNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

func fastSetPathFor(cfg string, getenv func(string) string) string {
	tier := tierPathFor(cfg, getenv)
	if tier == "" {
		return ""
	}
	return strings.TrimSuffix(tier, tierFile) + fastSetFile
}

// readFastSetFile: absent, unreadable or not a name means no preset.
func readFastSetFile(read func(string) ([]byte, error), path string) string {
	if path == "" {
		return ""
	}
	b, err := read(path)
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(b))
	if !fastSetNameRE.MatchString(name) {
		return ""
	}
	return name
}

// fastSetFor is the preset a search sends: the user's, only when the search
// runs on the fast tier, and only when the router lists that preset. The router
// refuses a field it does not know, so a router that predates presets would
// fail the whole search over one; asking first costs a round trip, paid only
// by a participant who stored a preset and searched on the fast tier, and any
// failure to ask sends none (the router's default preset applies).
func fastSetFor(ctx context.Context, tier, cfg, routerBase, key string, getenv func(string) string) string {
	if tier != "fast" {
		return ""
	}
	name := readFastSetFile(readTierSetting, fastSetPathFor(cfg, getenv))
	if name == "" || !routerListsFastSet(ctx, routerBase, key, name) {
		return ""
	}
	return name
}

const fastSetsProbeTimeout = 2 * time.Second

// routerListsFastSet: does GET /v1/fast-sets answer with a list that names
// this preset? Anything else (an older router, a 200 that is not the list, a
// preset renamed or withdrawn) sends none, because the router refuses a
// preset it does not know and the agent could not fix that.
func routerListsFastSet(ctx context.Context, routerBase, key, name string) bool {
	ctx, cancel := context.WithTimeout(ctx, fastSetsProbeTimeout)
	defer cancel()
	doc, err := getFastSets(ctx, routerClient(), routerBase, key)
	return err == nil && doc.lists(name)
}
