package main

// The two tools `jevlin mcp` lists, and their input schemas.
//
// Each schema describes the version-1 request its machine path accepts,
// field for field: the property names are held to wireSearchRequest's and
// wireFetchRequest's json tags by TestTheToolSchemasAreTheMachineContract,
// so a field added to the request and not here, or here and not to the
// request, fails a test. The bounds repeat the validators' so a client that
// checks a schema refuses what the machine path would refuse anyway; the
// machine path stays the judge, and its answer is what a call returns.
//
// The descriptions say what to do, not how the router works, and name no
// tier and no price: the user's own tier applies when the agent leaves
// "tier" out, which is what the agent is asked to do.

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func mcpTools() []mcpTool {
	return []mcpTool{
		{
			Name: "search",
			Description: "Search the public web: one call asks several search indexes and returns one de-duplicated list " +
				"in which each page names the indexes that found it. Put other wordings of the same question in " +
				"search_queries rather than making another call, then read the most promising pages with the fetch " +
				"tool by their source_id. Leave tier out and the user's own tier applies.",
			InputSchema: searchInputSchema(),
		},
		{
			Name: "fetch",
			Description: "Read pages from your recent searches in full, up to five per call, each by the source_id a " +
				"search returned or by its url. Set query to the fact you need and each page's text starts at the " +
				"part that matches it.",
			InputSchema: fetchInputSchema(),
		},
	}
}

func stringProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func intProp(desc string, minimum int, maximum int) map[string]any {
	p := map[string]any{"type": "integer", "description": desc, "minimum": minimum}
	if maximum > 0 {
		p["maximum"] = maximum
	}
	return p
}

func stringListProp(desc string, maxItems int) map[string]any {
	return map[string]any{"type": "array", "description": desc, "items": map[string]any{"type": "string"}, "maxItems": maxItems}
}

func versionProp() map[string]any {
	return map[string]any{"type": "integer", "description": "The request version; 1, and 1 when left out.", "enum": []int{1}}
}

func searchInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"query"},
		"properties": map[string]any{
			"version": versionProp(),
			"query": stringProp("What you are looking for, phrased the way a page that answers it would phrase it, " +
				"with the distinctive names, numbers and dates in it."),
			"objective":      stringProp("One sentence on what you are trying to find out."),
			"search_queries": stringListProp("Other wordings, searched in the same call and merged into the same list.", searchQueriesMax),
			"tier":           stringProp("Leave it out: the user's own tier applies."),
			"recency": map[string]any{"type": "string", "enum": []string{"day", "week", "month", "year"},
				"description": "Keep pages published within this period; pages with no date are kept."},
			"domain_filter": stringListProp("Bare hostnames, when the answer lives on known sites.", domainFilterMax),
			"max_results":   intProp("Results each index returns.", 1, 25),
			"providers":     stringListProp("Named indexes; only when the user asks for a specific index.", providersMax),
			"view": map[string]any{"type": "string", "enum": []string{"merged", "full"},
				"description": `"merged" (the default) says each page once; "full" adds every index's own answer.`},
			"merged_limit": intProp("How many pages the list holds.", 1, mergedLimitMax),
			"max_chars":    intProp("One character budget for the excerpts of the whole list.", 1, 0),
			"page_chars":   intProp("A cap on each page's excerpt.", pageCharsMin, pageCharsMax),
			"cache": map[string]any{"type": "string", "enum": []string{"default", "refresh"},
				"description": `"refresh" runs the search live rather than from a recent cached result, for questions about what is happening now.`},
		},
	}
}

func fetchInputSchema() map[string]any {
	page := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"description":          "Exactly one of source_id or url.",
		"properties": map[string]any{
			"source_id": stringProp("A page's source_id from a search result."),
			"url":       stringProp("An http or https URL from a search result."),
		},
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"pages"},
		"properties": map[string]any{
			"version": versionProp(),
			"pages": map[string]any{"type": "array", "items": page, "minItems": 1, "maxItems": fetchPagesMax,
				"description": "The pages to read."},
			"max_chars": intProp("Characters of each page to return.", fetchMaxCharsMin, fetchMaxCharsMax),
			"offset":    intProp("Where to start reading: a page's next_offset from an earlier fetch.", 0, 0),
			"query":     stringProp("The fact you need; each page's text starts at the part that matches it."),
		},
	}
}
