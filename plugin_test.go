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
		CollectionName: "articles",
		Fields:         []string{"id", "title", "body"},
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
		CollectionName: "articles",
		Fields:         []string{"id", "title"},
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
			cfg.Set(configField, `[{"collection_name":"articles","fields":["title","body"]}]`)

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
