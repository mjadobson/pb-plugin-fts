package fts

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/types"
)

func TestParsePluginConfigsAcceptsJSONString(t *testing.T) {
	collection := core.NewBaseCollection(pluginsCollectionName)
	record := core.NewRecord(collection)
	record.Set(configField, `[{"collection_name":"articles","fields":["title"]}]`)

	configs, err := parsePluginConfigs(record)
	if err != nil {
		t.Fatalf("parsePluginConfigs returned error: %v", err)
	}

	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(configs))
	}

	if configs[0].CollectionName != "articles" {
		t.Fatalf("expected collection_name %q, got %q", "articles", configs[0].CollectionName)
	}
}

func TestNormalizeConfigSetsDefaultWeightsForIndexedFields(t *testing.T) {
	collection := core.NewBaseCollection("articles")
	collection.Fields.Add(&core.TextField{Name: "title"})
	collection.Fields.Add(&core.TextField{Name: "body"})

	p := &Plugin{}
	cfg := p.normalizeConfig(FTSConfig{
		CollectionName: "articles",
		Fields:         []string{"title", "body"},
		FieldWeights: map[string]float64{
			"title": 5,
		},
	}, collection)

	if cfg.FieldWeights["title"] != 5 {
		t.Fatalf("expected title weight 5, got %v", cfg.FieldWeights["title"])
	}

	if cfg.FieldWeights["body"] != 1 {
		t.Fatalf("expected body weight 1, got %v", cfg.FieldWeights["body"])
	}

	if cfg.FieldWeights["id"] != 1 {
		t.Fatalf("expected id weight 1, got %v", cfg.FieldWeights["id"])
	}
}

func TestNormalizeConfigSortsAndDeduplicatesPrefixes(t *testing.T) {
	collection := core.NewBaseCollection("articles")
	collection.Fields.Add(&core.TextField{Name: "title"})

	p := &Plugin{}
	cfg := p.normalizeConfig(FTSConfig{
		CollectionName: "articles",
		Fields:         []string{"title"},
		Prefixes:       []int{3, 2, 3, 1},
	}, collection)

	if len(cfg.Prefixes) != 3 {
		t.Fatalf("expected 3 prefixes, got %#v", cfg.Prefixes)
	}

	expected := []int{1, 2, 3}
	for i, prefix := range expected {
		if cfg.Prefixes[i] != prefix {
			t.Fatalf("expected prefixes %#v, got %#v", expected, cfg.Prefixes)
		}
	}
}

func TestFtsRankExpressionUsesFieldOrder(t *testing.T) {
	p := &Plugin{}

	rankExpr := p.ftsRankExpression(FTSConfig{
		Fields: []string{"id", "title", "body"},
		FieldWeights: map[string]float64{
			"title": 10,
			"body": 2,
		},
	})

	if rankExpr != "bm25(1, 10, 2)" {
		t.Fatalf("expected weighted bm25 expression, got %q", rankExpr)
	}
}

func TestParseFtsAuxOptionsDefaultsAndValidation(t *testing.T) {
	p := &Plugin{}
	cfg := FTSConfig{
		CollectionName:  "articles",
		Fields:          []string{"id", "title", "body"},
		AllowHighlights: true,
		AllowSnippets:   true,
	}

	req := &http.Request{
		URL: &url.URL{
			RawQuery: "search=ghosts&highlight=title,body&snippet=body&snippetTokens=12",
		},
	}

	opts, err := p.parseFtsAuxOptions(req, cfg)
	if err != nil {
		t.Fatalf("parseFtsAuxOptions returned error: %v", err)
	}

	if len(opts.HighlightFields) != 2 || opts.HighlightFields[0] != "title" || opts.HighlightFields[1] != "body" {
		t.Fatalf("unexpected highlight fields: %#v", opts.HighlightFields)
	}

	if len(opts.SnippetFields) != 1 || opts.SnippetFields[0] != "body" {
		t.Fatalf("unexpected snippet fields: %#v", opts.SnippetFields)
	}

	if opts.HighlightBefore != "<b>" || opts.HighlightAfter != "</b>" {
		t.Fatalf("unexpected highlight defaults: %q %q", opts.HighlightBefore, opts.HighlightAfter)
	}

	if opts.SnippetBefore != "<b>" || opts.SnippetAfter != "</b>" || opts.SnippetEllipsis != "..." {
		t.Fatalf("unexpected snippet defaults: %q %q %q", opts.SnippetBefore, opts.SnippetAfter, opts.SnippetEllipsis)
	}

	if opts.SnippetTokens != 12 {
		t.Fatalf("expected snippet token override, got %d", opts.SnippetTokens)
	}
}

func TestParseFtsAuxOptionsRequiresSearchAndIndexedFields(t *testing.T) {
	p := &Plugin{}
	cfg := FTSConfig{
		CollectionName:  "articles",
		Fields:          []string{"id", "title"},
		AllowHighlights: true,
		AllowSnippets:   true,
	}

	reqWithoutSearch := &http.Request{
		URL: &url.URL{
			RawQuery: "highlight=title",
		},
	}

	if _, err := p.parseFtsAuxOptions(reqWithoutSearch, cfg); err == nil {
		t.Fatalf("expected missing search error")
	}

	reqWithUnknownField := &http.Request{
		URL: &url.URL{
			RawQuery: "search=ghosts&snippet=body",
		},
	}

	if _, err := p.parseFtsAuxOptions(reqWithUnknownField, cfg); err == nil {
		t.Fatalf("expected unknown snippet field error")
	}
}

func TestParseFtsAuxOptionsRequiresEnabledFeatures(t *testing.T) {
	p := &Plugin{}

	highlightReq := &http.Request{
		URL: &url.URL{
			RawQuery: "search=ghosts&highlight=title",
		},
	}

	if _, err := p.parseFtsAuxOptions(highlightReq, FTSConfig{
		CollectionName: "articles",
		Fields:         []string{"id", "title"},
	}); err == nil {
		t.Fatalf("expected disabled highlight error")
	}

	snippetReq := &http.Request{
		URL: &url.URL{
			RawQuery: "search=ghosts&snippet=title",
		},
	}

	if _, err := p.parseFtsAuxOptions(snippetReq, FTSConfig{
		CollectionName: "articles",
		Fields:         []string{"id", "title"},
	}); err == nil {
		t.Fatalf("expected disabled snippet error")
	}
}

func TestNormalizeSearchQueryDowngradesShortPrefixQueries(t *testing.T) {
	p := &Plugin{}

	got := p.normalizeSearchQuery("mo* moon* OR stars*", FTSConfig{
		MinPrefixQueryLength: 3,
	})

	if got != "mo moon* OR stars*" {
		t.Fatalf("expected short prefix query to be downgraded, got %q", got)
	}

	got = p.normalizeSearchQuery("moon*", FTSConfig{})
	if got != "moon" {
		t.Fatalf("expected prefix query to be downgraded by default, got %q", got)
	}
}

func TestFtsTableOptionsIncludePrefixConfiguration(t *testing.T) {
	p := &Plugin{}

	options := p.ftsTableOptions(FTSConfig{
		Tokenizer: "porter",
		Prefixes:  []int{2, 3},
	}, []string{"_fts_id", "_fts_title"})

	expected := []string{
		"_fts_id",
		"_fts_title",
		`tokenize="porter"`,
		`prefix='2 3'`,
	}

	if len(options) != len(expected) {
		t.Fatalf("expected options %#v, got %#v", expected, options)
	}

	for i, option := range expected {
		if options[i] != option {
			t.Fatalf("expected options %#v, got %#v", expected, options)
		}
	}
}

func TestSQLiteStringLiteralEscapesQuotes(t *testing.T) {
	got := sqliteStringLiteral("o'clock")

	if got != "'o''clock'" {
		t.Fatalf("expected escaped sqlite literal, got %q", got)
	}
}

func TestFtsRouteIncludesAuxFieldsInResponse(t *testing.T) {
	scenario := tests.ApiScenario{
		Name:   "fts route returns snippet and highlight custom fields",
		Method: http.MethodGet,
		URL:    "/api/collections/articles/records/fts?search=ghosts&highlight=title&snippet=body",
		TestAppFactory: func(t testing.TB) *tests.TestApp {
			app, err := tests.NewTestApp()
			if err != nil {
				t.Fatalf("failed to create test app: %v", err)
			}

			p := &Plugin{}
			if err := p.Init(app); err != nil {
				t.Fatalf("failed to init plugin: %v", err)
			}

			articles := core.NewBaseCollection("articles")
			articles.ListRule = types.Pointer("1=1")
			articles.Fields.Add(&core.TextField{Name: "title"})
			articles.Fields.Add(&core.TextField{Name: "body"})

			if err := app.Save(articles); err != nil {
				t.Fatalf("failed to save articles collection: %v", err)
			}

			plugins, err := app.FindCollectionByNameOrId(pluginsCollectionName)
			if err != nil {
				t.Fatalf("failed to find plugins collection: %v", err)
			}

			cfg := core.NewRecord(plugins)
			cfg.Set(pluginNameField, p.Name())
			cfg.Set(enabledField, true)
			cfg.Set(configField, `[{"collection_name":"articles","fields":["title","body"],"allow_highlights":true,"allow_snippets":true}]`)

			if err := app.Save(cfg); err != nil {
				t.Fatalf("failed to save plugin config: %v", err)
			}

			record := core.NewRecord(articles)
			record.Set("title", "Ghost stories")
			record.Set("body", "A ghost appears in the old house every winter night.")

			if err := app.Save(record); err != nil {
				t.Fatalf("failed to save article record: %v", err)
			}

			if err := p.refreshState(app); err != nil {
				t.Fatalf("failed to refresh plugin state: %v", err)
			}

			return app
		},
		ExpectedStatus: http.StatusOK,
		ExpectedContent: []string{
			`"_fts_highlight_title":"\u003cb\u003eGhost\u003c/b\u003e stories"`,
			`"_fts_snippet_body":"A \u003cb\u003eghost\u003c/b\u003e appears in the old house every winter night."`,
		},
	}

	scenario.Test(t)
}

func TestFtsRouteIncludesAuxFieldsEvenWhenFieldsQueryOmitsThem(t *testing.T) {
	scenario := tests.ApiScenario{
		Name:   "fts route forces aux fields into projected response",
		Method: http.MethodGet,
		URL:    "/api/collections/articles/records/fts?search=ghosts&snippet=title&fields=id,title",
		TestAppFactory: func(t testing.TB) *tests.TestApp {
			app, err := tests.NewTestApp()
			if err != nil {
				t.Fatalf("failed to create test app: %v", err)
			}

			p := &Plugin{}
			if err := p.Init(app); err != nil {
				t.Fatalf("failed to init plugin: %v", err)
			}

			articles := core.NewBaseCollection("articles")
			articles.ListRule = types.Pointer("1=1")
			articles.Fields.Add(&core.TextField{Name: "title"})
			articles.Fields.Add(&core.TextField{Name: "body"})

			if err := app.Save(articles); err != nil {
				t.Fatalf("failed to save articles collection: %v", err)
			}

			plugins, err := app.FindCollectionByNameOrId(pluginsCollectionName)
			if err != nil {
				t.Fatalf("failed to find plugins collection: %v", err)
			}

			cfg := core.NewRecord(plugins)
			cfg.Set(pluginNameField, p.Name())
			cfg.Set(enabledField, true)
			cfg.Set(configField, `[{"collection_name":"articles","fields":["title","body"],"allow_snippets":true}]`)

			if err := app.Save(cfg); err != nil {
				t.Fatalf("failed to save plugin config: %v", err)
			}

			record := core.NewRecord(articles)
			record.Set("title", "Ghost stories")
			record.Set("body", "A ghost appears in the old house every winter night.")

			if err := app.Save(record); err != nil {
				t.Fatalf("failed to save article record: %v", err)
			}

			if err := p.refreshState(app); err != nil {
				t.Fatalf("failed to refresh plugin state: %v", err)
			}

			return app
		},
		ExpectedStatus: http.StatusOK,
		ExpectedContent: []string{
			`"_fts_snippet_title":"\u003cb\u003eGhost\u003c/b\u003e stories"`,
			`"id":"`,
			`"title":"Ghost stories"`,
		},
	}

	scenario.Test(t)
}

func TestFtsRouteDowngradesPrefixQueriesWhenBelowMinimum(t *testing.T) {
	scenario := tests.ApiScenario{
		Name:   "fts route downgrades short prefix queries to non-prefix search",
		Method: http.MethodGet,
		URL:    "/api/collections/articles/records/fts?search=ghosts*",
		TestAppFactory: func(t testing.TB) *tests.TestApp {
			app, err := tests.NewTestApp()
			if err != nil {
				t.Fatalf("failed to create test app: %v", err)
			}

			p := &Plugin{}
			if err := p.Init(app); err != nil {
				t.Fatalf("failed to init plugin: %v", err)
			}

			articles := core.NewBaseCollection("articles")
			articles.ListRule = types.Pointer("1=1")
			articles.Fields.Add(&core.TextField{Name: "title"})

			if err := app.Save(articles); err != nil {
				t.Fatalf("failed to save articles collection: %v", err)
			}

			plugins, err := app.FindCollectionByNameOrId(pluginsCollectionName)
			if err != nil {
				t.Fatalf("failed to find plugins collection: %v", err)
			}

			cfg := core.NewRecord(plugins)
			cfg.Set(pluginNameField, p.Name())
			cfg.Set(enabledField, true)
			cfg.Set(configField, `[{"collection_name":"articles","fields":["title"],"min_prefix_query_length":10}]`)

			if err := app.Save(cfg); err != nil {
				t.Fatalf("failed to save plugin config: %v", err)
			}

			record := core.NewRecord(articles)
			record.Set("title", "Ghost stories")

			if err := app.Save(record); err != nil {
				t.Fatalf("failed to save article record: %v", err)
			}

			if err := p.refreshState(app); err != nil {
				t.Fatalf("failed to refresh plugin state: %v", err)
			}

			return app
		},
		ExpectedStatus: http.StatusOK,
		ExpectedContent: []string{
			`"title":"Ghost stories"`,
		},
	}

	scenario.Test(t)
}
