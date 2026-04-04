package fts

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
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
