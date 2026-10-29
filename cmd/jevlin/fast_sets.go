package main

// The fast tier's presets: which indexes a fast search asks, chosen by the
// user (`jevlin agents tier fast <preset>`) and listed by the router
// (GET /v1/fast-sets). The client holds only the name; the router owns the
// list and its prices, so a client that cannot reach the list still sends
// the stored name and says that prices are unavailable. The request carries
// the participant's sr- key to [router] url, shaped like limits': configured
// origin, same-origin redirects, bounded body.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jevlinai/jevlin-go/pkg/auth"
)

// errFastSetsAbsent: the router answered, and has no presets.
var errFastSetsAbsent = errors.New("this router does not list presets")

const fastSetsTimeout = 10 * time.Second

type fastSetsDoc struct {
	Default string        `json:"default"`
	Sets    []fastSetInfo `json:"sets"`
}

type fastSetInfo struct {
	Name                string   `json:"name"`
	Providers           []string `json:"providers"`
	EstimatedCostMicros int64    `json:"estimated_cost_micros"`
}

// fetchFastSets reads the router's presets. Any failure is an error the
// caller turns into "prices are unavailable": status must work offline.
func fetchFastSets(cfgPath string, getenv func(string) string) (fastSetsDoc, error) {
	var doc fastSetsDoc
	cfg, _, err := loadConfig(cfgPath, getenv)
	if err != nil {
		return doc, err
	}
	if cfg.Search.RouterURL == nil {
		return doc, fmt.Errorf("no router configured")
	}
	key, keySrc, err := resolveAPIKey(getenv, cfg.Search)
	if err == nil && keySrc == keyFromEnv {
		err = envKeyRefusedFor(cfgPath, cfg, getenv)
	}
	if err != nil {
		return doc, err
	}
	if key == "" {
		return doc, fmt.Errorf("no search credential")
	}
	ctx, cancel := context.WithTimeout(context.Background(), fastSetsTimeout)
	defer cancel()
	client := &http.Client{Timeout: fastSetsTimeout, CheckRedirect: auth.SameOriginRedirects, Transport: loginProbeTransport}
	return getFastSets(ctx, client, cfg.Search.RouterURL.String(), key)
}

// getFastSets is the one GET /v1/fast-sets, for `agents tier` and for the
// search's check that its stored preset is still offered. The client is the
// caller's, with the redirect policy a request carrying the sr- key needs.
func getFastSets(ctx context.Context, client *http.Client, routerBase, key string) (fastSetsDoc, error) {
	var doc fastSetsDoc
	endpoint := strings.TrimRight(routerBase, "/") + "/v1/fast-sets"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return doc, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", searchUserAgent+"/"+strings.TrimPrefix(buildVersion(), "v"))
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		return doc, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return doc, errFastSetsAbsent
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return doc, fmt.Errorf("the router answered HTTP %d", resp.StatusCode)
	}
	raw, err := readBoundedBody(resp.Body, searchMaxBody)
	if err != nil {
		return doc, err
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return doc, fmt.Errorf("the router's preset list could not be read")
	}
	return doc, nil
}

// lists: is name one of the presets the router offers? An answer that names
// none (an empty object, a body of another shape) lists nothing.
func (d fastSetsDoc) lists(name string) bool {
	for _, s := range d.Sets {
		if s.Name == name {
			return true
		}
	}
	return false
}

// names are the presets the router offers that look like names.
func (d fastSetsDoc) names() []string {
	var out []string
	for _, s := range d.Sets {
		if fastSetNameRE.MatchString(s.Name) {
			out = append(out, s.Name)
		}
	}
	return out
}

// printFastSets says which preset is stored and lists what the router
// offers, with its estimated price per search. The stored name is repeated
// through the name check, never raw.
func printFastSets(stdout io.Writer, stored string, doc fastSetsDoc, err error) {
	if errors.Is(err, errFastSetsAbsent) {
		fmt.Fprintln(stdout, "fast presets: not available on this router, so none is sent and its default fast tier applies")
		return
	}
	if err != nil || len(doc.Sets) == 0 {
		name := "the router's default"
		if stored != "" {
			name = stored
		}
		fmt.Fprintf(stdout, "fast preset: %s (prices are unavailable: the router's preset list could not be read)\n", name)
		return
	}
	name := stored
	if name == "" {
		name = doc.Default + " (the router's default)"
	}
	fmt.Fprintf(stdout, "fast preset: %s\n", name)
	if stored != "" && !doc.lists(stored) {
		fmt.Fprintf(stdout, "  warning: this router does not list %q (it lists %s), so no preset is sent; set one with `jevlin agents tier fast <preset>`\n", stored, strings.Join(doc.names(), ", "))
	}
	for _, s := range doc.Sets {
		if !fastSetNameRE.MatchString(s.Name) {
			continue
		}
		fmt.Fprintf(stdout, "  %-10s %s; estimated %s per search\n", s.Name, strings.Join(safeWords(s.Providers), ", "), dollars(s.EstimatedCostMicros))
	}
}

// safeWords keeps the provider names that look like names.
func safeWords(in []string) []string {
	var out []string
	for _, w := range in {
		if fastSetNameRE.MatchString(w) {
			out = append(out, w)
		}
	}
	return out
}
