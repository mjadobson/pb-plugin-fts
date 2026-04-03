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
