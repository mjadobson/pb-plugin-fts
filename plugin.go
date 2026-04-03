package fts

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	_ "github.com/mjadobson/pb-plugin-fts/migrations"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/search"
	"github.com/pocketbase/pocketbase/tools/types"
	"github.com/pocketbuilds/xpb"
)

const pluginsCollectionName = "_plugins"
const pluginNameField = "plugin_name"
const configField = "config"
const enabledField = "enabled"
const defaultTokenizer = "porter"

// FTSConfig represents a single FTS configuration for a collection
type FTSConfig struct {
	CollectionName string   `json:"collection_name"`
	Fields         []string `json:"fields"`
	Tokenizer      string   `json:"tokenizer"`
}

func init() {
	xpb.Register(&Plugin{})
}

type Plugin struct {
	state *pluginState
}

type pluginState struct {
	mu     sync.RWMutex
	config map[string][]FTSConfig // collection -> configs
}

func newPluginState() *pluginState {
	return &pluginState{
		config: make(map[string][]FTSConfig),
	}
}

func (p *Plugin) Name() string {
	return "fts"
}

var version string

func (p *Plugin) Version() string {
	return version
}

func (p *Plugin) Description() string {
	return "Add full text search capabilities"
}

func (p *Plugin) Init(app core.App) error {
	p.state = newPluginState()

	if app.IsBootstrapped() {
		if err := p.initialize(app); err != nil {
			return err
		}
	} else {
		app.OnBootstrap().BindFunc(func(e *core.BootstrapEvent) error {
			if err := e.Next(); err != nil {
				return err
			}
			return p.initialize(e.App)
		})
	}

	app.OnServe().BindFunc(p.setupFtsRoute)

	// Validate FTS configs when they change
	app.OnRecordValidate(pluginsCollectionName).BindFunc(p.validatePluginsConfigField)

	// Reload state when FTS configs change
	refreshConfigs := func(e *core.RecordEvent) error {
		if e.Record.GetString(pluginNameField) == p.Name() {
			if err := p.refreshState(e.App); err != nil {
				e.App.Logger().Error("fts config reload failed", "error", err)
			}
		}
		return e.Next()
	}

	app.OnRecordAfterCreateSuccess(pluginsCollectionName).BindFunc(refreshConfigs)
	app.OnRecordAfterUpdateSuccess(pluginsCollectionName).BindFunc(refreshConfigs)
	app.OnRecordAfterDeleteSuccess(pluginsCollectionName).BindFunc(refreshConfigs)

	// Handle collection changes that might affect FTS configs
	refreshOnCollectionChange := func(e *core.CollectionEvent) error {
		if err := p.refreshState(e.App); err != nil {
			e.App.Logger().Error("fts config reload failed", "error", err)
		}
		return e.Next()
	}

	app.OnCollectionAfterCreateSuccess().BindFunc(refreshOnCollectionChange)
	app.OnCollectionAfterUpdateSuccess().BindFunc(refreshOnCollectionChange)
	app.OnCollectionAfterDeleteSuccess().BindFunc(refreshOnCollectionChange)

	return nil
}

func (p *Plugin) initialize(app core.App) error {
	if err := ensurePluginsCollection(app); err != nil {
		return err
	}

	return p.refreshState(app)
}

func (p *Plugin) setupFtsRoute(e *core.ServeEvent) error {
	e.Router.GET("/api/collections/{collection}/records/fts", func(e *core.RequestEvent) error {
		collection, err := e.App.FindCachedCollectionByNameOrId(e.Request.PathValue("collection"))
		if err != nil || collection == nil {
			return e.NotFoundError("Missing collection context.", err)
		}

		requestInfo, err := e.RequestInfo()
		if err != nil {
			return err
		}

		if collection.ListRule == nil && !requestInfo.HasSuperuserAuth() {
			return e.ForbiddenError("Only superusers can perform this action.", nil)
		}

		// Check if collection has FTS configured
		configs := p.getConfigsForCollection(collection.Name)
		if len(configs) == 0 {
			return e.NotFoundError("Full text search not configured for this collection.", nil)
		}

		fieldsResolver := core.NewRecordFieldResolver(
			e.App,
			collection,
			requestInfo,
			requestInfo.HasSuperuserAuth(),
		)

		query := e.App.RecordQuery(collection)

		ftsTableName := p.ftsTableNameFromCollectionName(collection.Name)

		if search := e.Request.URL.Query().Get("search"); search != "" {
			query.InnerJoin(ftsTableName, dbx.NewExp(fmt.Sprintf(
				"%s = id",
				p.ftsFieldName("id"),
			)))

			query.AndWhere(dbx.NewExp(fmt.Sprintf(
				"%s MATCH {:search}", ftsTableName,
			), dbx.Params{
				"search": search,
			}))

			if e.Request.URL.Query().Get("sort") == "" {
				query.OrderBy("rank")
			}
		}

		searchProvider := search.NewProvider(fieldsResolver).
			Query(query)

		if !requestInfo.HasSuperuserAuth() && collection.ListRule != nil {
			searchProvider.AddFilter(search.FilterData(*collection.ListRule))
		}

		records := []*core.Record{}

		result, err := searchProvider.ParseAndExec(e.Request.URL.Query().Encode(), &records)
		if err != nil {
			return err
		}

		if err := apis.EnrichRecords(e, records); err != nil {
			return e.InternalServerError("Failed to enrich records", err)
		}

		return e.JSON(http.StatusOK, result)
	})
	return e.Next()
}

func (p *Plugin) refreshState(app core.App) error {
	records, err := app.FindAllRecords(pluginsCollectionName, dbx.HashExp{
		pluginNameField: p.Name(),
		enabledField:    true,
	})
	if err != nil {
		return fmt.Errorf("load FTS configs: %w", err)
	}

	next := make(map[string][]FTSConfig)

	for _, record := range records {
		configs, err := parsePluginConfigs(record)
		if err != nil {
			app.Logger().Error("fts invalid config", "recordId", record.Id, "error", err)
			continue
		}

		seenCollections := make(map[string]int, len(configs))
		for i, cfg := range configs {
			if err := validateConfigShape(cfg); err != nil {
				app.Logger().Error("fts invalid config shape", "recordId", record.Id, "configIndex", i, "error", err)
				continue
			}

			collection, err := app.FindCollectionByNameOrId(cfg.CollectionName)
			if err != nil {
				app.Logger().Warn("fts collection not found", "recordId", record.Id, "collection", cfg.CollectionName, "error", err)
				if _, exists := seenCollections[cfg.CollectionName]; exists {
					app.Logger().Warn("fts duplicate config skipped", "recordId", record.Id, "configIndex", i, "collection", cfg.CollectionName)
					continue
				}
				seenCollections[cfg.CollectionName] = i
				next[cfg.CollectionName] = append(next[cfg.CollectionName], cfg)
				continue
			}

			cfg = p.normalizeConfig(cfg, collection)

			if err := p.validateConfigForCollection(cfg, collection); err != nil {
				app.Logger().Error("fts invalid collection config", "recordId", record.Id, "configIndex", i, "collection", cfg.CollectionName, "error", err)
				continue
			}

			if _, exists := seenCollections[cfg.CollectionName]; exists {
				app.Logger().Warn("fts duplicate config skipped", "recordId", record.Id, "configIndex", i, "collection", cfg.CollectionName)
				continue
			}
			seenCollections[cfg.CollectionName] = i

			// Ensure FTS table exists
			if err := p.createFtsTable(app, cfg); err != nil {
				app.Logger().Error("fts table creation failed", "recordId", record.Id, "configIndex", i, "collection", cfg.CollectionName, "error", err)
				continue
			}

			next[cfg.CollectionName] = append(next[cfg.CollectionName], cfg)
		}
	}

	p.state.mu.Lock()
	p.state.config = next
	p.state.mu.Unlock()

	return nil
}

func validateConfigShape(cfg FTSConfig) error {
	if cfg.CollectionName == "" {
		return errors.New("collection_name is required")
	}
	return nil
}

func (p *Plugin) normalizeConfig(cfg FTSConfig, collection *core.Collection) FTSConfig {
	if len(cfg.Fields) == 0 {
		for key := range core.NewRecord(collection).PublicExport() {
			if key != core.FieldNameCollectionId && key != core.FieldNameCollectionName {
				cfg.Fields = append(cfg.Fields, key)
			}
		}
	}

	if !slices.Contains(cfg.Fields, "id") {
		cfg.Fields = append([]string{"id"}, cfg.Fields...)
	}

	if cfg.Tokenizer == "" {
		cfg.Tokenizer = defaultTokenizer
	}

	return cfg
}

func (p *Plugin) validateConfigForCollection(cfg FTSConfig, collection *core.Collection) error {
	for _, fieldName := range cfg.Fields {
		field := collection.Fields.GetByName(fieldName)
		if field == nil {
			return fmt.Errorf("field %q not found in collection %q", fieldName, cfg.CollectionName)
		}
	}

	// Prevent indexing password and token fields in auth collections
	if collection.IsAuth() {
		for _, fieldName := range cfg.Fields {
			if fieldName == core.FieldNamePassword || fieldName == core.FieldNameTokenKey {
				return fmt.Errorf("field %q cannot be indexed in auth collection %q", fieldName, cfg.CollectionName)
			}
		}
	}

	return nil
}

func (p *Plugin) getConfigsForCollection(collectionName string) []FTSConfig {
	p.state.mu.RLock()
	defer p.state.mu.RUnlock()

	configs := p.state.config[collectionName]
	if len(configs) == 0 {
		return nil
	}

	// Return a copy to prevent external modification
	cloned := make([]FTSConfig, len(configs))
	copy(cloned, configs)
	return cloned
}

func (p *Plugin) createFtsTable(app core.App, config FTSConfig) error {
	ftsTableName := p.ftsTableNameFromCollectionName(config.CollectionName)

	ftsFieldNames := p.ftsFieldNameSlice(config.Fields)

	var query *dbx.Query

	query = app.DB().NewQuery(fmt.Sprintf(
		`DROP TABLE IF EXISTS %s;`,
		ftsTableName,
	))
	if _, err := query.Execute(); err != nil {
		return err
	}

	query = app.DB().NewQuery(fmt.Sprintf(
		`CREATE VIRTUAL TABLE %s USING fts5 (%s, tokenize="%s");`,
		ftsTableName,
		strings.Join(ftsFieldNames, ", "),
		config.Tokenizer,
	))
	if _, err := query.Execute(); err != nil {
		if strings.Contains(err.Error(), "no such tokenizer") {
			return validation.Errors{
				"config": validation.NewError("invalid_tokenizer", "no such tokenizer"),
			}
		}
		return err
	}

	query = app.DB().NewQuery(fmt.Sprintf(
		`INSERT INTO %s (%s) SELECT %s FROM %s;`,
		ftsTableName,
		strings.Join(ftsFieldNames, ", "),
		strings.Join(config.Fields, ", "),
		config.CollectionName,
	))
	if _, err := query.Execute(); err != nil {
		return err
	}

	query = app.DB().NewQuery(fmt.Sprintf(
		`DROP TRIGGER IF EXISTS insert_%s;`,
		ftsTableName,
	))
	if _, err := query.Execute(); err != nil {
		return err
	}

	query = app.DB().NewQuery(fmt.Sprintf(
		`CREATE TRIGGER IF NOT EXISTS insert_%s AFTER INSERT ON %s BEGIN %s END;`,
		ftsTableName,
		config.CollectionName,
		fmt.Sprintf(
			`INSERT INTO %s (%s) SELECT %s FROM %s WHERE id = NEW.id;`,
			ftsTableName,
			strings.Join(ftsFieldNames, ", "),
			strings.Join(config.Fields, ", "),
			config.CollectionName,
		),
	))
	if _, err := query.Execute(); err != nil {
		return err
	}

	query = app.DB().NewQuery(fmt.Sprintf(
		`DROP TRIGGER IF EXISTS update_%s;`,
		ftsTableName,
	))
	if _, err := query.Execute(); err != nil {
		return err
	}

	query = app.DB().NewQuery(fmt.Sprintf(
		`CREATE TRIGGER IF NOT EXISTS update_%s AFTER UPDATE ON %s BEGIN %s END;`,
		ftsTableName,
		config.CollectionName,
		fmt.Sprintf(
			`UPDATE %s SET %s WHERE %s = NEW.id;`,
			ftsTableName,
			func() string {
				results := make([]string, 0, len(ftsFieldNames))
				for i, ftsFieldName := range ftsFieldNames {
					fieldName := config.Fields[i]
					results = append(results, fmt.Sprintf(
						"%s = NEW.%s",
						ftsFieldName, fieldName,
					))
				}
				return strings.Join(results, ", ")
			}(),
			p.ftsFieldName("id"),
		),
	))
	if _, err := query.Execute(); err != nil {
		return err
	}

	query = app.DB().NewQuery(fmt.Sprintf(
		`DROP TRIGGER IF EXISTS delete_%s;`,
		ftsTableName,
	))
	if _, err := query.Execute(); err != nil {
		return err
	}

	query = app.DB().NewQuery(fmt.Sprintf(
		`CREATE TRIGGER IF NOT EXISTS delete_%s AFTER DELETE ON %s BEGIN %s END;`,
		ftsTableName,
		config.CollectionName,
		fmt.Sprintf(
			`DELETE FROM %s WHERE %s = OLD.id;`,
			ftsTableName,
			p.ftsFieldName("id"),
		),
	))
	if _, err := query.Execute(); err != nil {
		return err
	}
	return nil
}

func (p *Plugin) dropFtsTable(app core.App, config FTSConfig) error {
	ftsTableName := p.ftsTableNameFromCollectionName(config.CollectionName)
	query := app.DB().NewQuery(fmt.Sprintf(
		`DROP TABLE IF EXISTS %s;`,
		ftsTableName,
	))
	if _, err := query.Execute(); err != nil {
		return err
	}
	query = app.DB().NewQuery(fmt.Sprintf(
		`DROP TRIGGER IF EXISTS insert_%s;`,
		ftsTableName,
	))
	if _, err := query.Execute(); err != nil {
		return err
	}
	query = app.DB().NewQuery(fmt.Sprintf(
		`DROP TRIGGER IF EXISTS update_%s;`,
		ftsTableName,
	))
	if _, err := query.Execute(); err != nil {
		return err
	}
	query = app.DB().NewQuery(fmt.Sprintf(
		`DROP TRIGGER IF EXISTS delete_%s;`,
		ftsTableName,
	))
	if _, err := query.Execute(); err != nil {
		return err
	}
	return nil
}

func (p *Plugin) ftsTableNameFromCollectionName(collectionName string) string {
	return fmt.Sprintf("_fts_%s", collectionName)
}

func (p *Plugin) ftsFieldName(fieldName string) string {
	return fmt.Sprintf("_fts_%s", fieldName)
}

func (p *Plugin) ftsFieldNameSlice(fieldNames []string) []string {
	result := make([]string, 0, len(fieldNames))
	for _, fieldName := range fieldNames {
		result = append(result, p.ftsFieldName(fieldName))
	}
	return result
}

func (p *Plugin) validatePluginsConfigField(e *core.RecordEvent) error {
	// Only validate FTS plugin configurations
	if e.Record.GetString(pluginNameField) != p.Name() {
		return e.Next()
	}

	err := e.Next()
	validationErrs := validation.Errors{}
	if e, ok := err.(validation.Errors); ok {
		validationErrs = e
	} else if err != nil {
		return err
	}

	config, err := parsePluginConfigs(e.Record)
	if err != nil {
		validationErrs["config"] = validation.NewError("expected_type_error", "must be an array of FTS configurations")
		return validationErrs
	}

	seenCollections := make(map[string]int, len(config))

	for i, cfg := range config {
		idxPrefix := fmt.Sprintf("config.%d", i)

		if err := validateConfigShape(cfg); err != nil {
			validationErrs[idxPrefix] = validation.NewError("invalid_config", err.Error())
			continue
		}

		// Validate collection exists
		collection, err := e.App.FindCachedCollectionByNameOrId(cfg.CollectionName)
		if err != nil {
			validationErrs[idxPrefix+".collection_name"] = validation.NewError("not_found", "collection does not exist")
			continue
		}

		cfg = p.normalizeConfig(cfg, collection)

		if err := p.validateConfigForCollection(cfg, collection); err != nil {
			validationErrs[idxPrefix] = validation.NewError("invalid_config", err.Error())
			continue
		}

		if firstIndex, exists := seenCollections[cfg.CollectionName]; exists {
			validationErrs[idxPrefix+".collection_name"] = validation.NewError(
				"duplicate_collection",
				fmt.Sprintf("collection already configured at config.%d", firstIndex),
			)
			continue
		}

		seenCollections[cfg.CollectionName] = i
		config[i] = cfg
	}

	// Update the config with normalized values
	if len(validationErrs) == 0 {
		configJSON, err := types.ParseJSONRaw(config)
		if err != nil {
			validationErrs["config"] = validation.NewError("encoding_error", "failed to encode configuration")
		} else {
			e.Record.Set("config", configJSON)
		}
	}

	if len(validationErrs) != 0 {
		return validationErrs
	}

	return nil
}

func ensurePluginsCollection(app core.App) error {
	collection, err := app.FindCollectionByNameOrId(pluginsCollectionName)
	if err != nil {
		collection = core.NewBaseCollection(pluginsCollectionName)
	}

	if collection.Fields.GetByName(pluginNameField) == nil {
		collection.Fields.Add(&core.TextField{Name: pluginNameField, Required: true})
	}

	if collection.Fields.GetByName(configField) == nil {
		collection.Fields.Add(&core.JSONField{Name: configField, Required: true})
	}

	if collection.Fields.GetByName(enabledField) == nil {
		collection.Fields.Add(&core.BoolField{Name: enabledField})
	}

	if !hasSingleColumnUniqueIndex(collection, pluginNameField) {
		collection.AddIndex("idx__plugins_plugin_name_unique", true, "`plugin_name`", "")
	}

	return app.Save(collection)
}

func hasSingleColumnUniqueIndex(collection *core.Collection, fieldName string) bool {
	for _, index := range collection.Indexes {
		if strings.Contains(index, "UNIQUE") && strings.Contains(index, "`"+fieldName+"`") {
			return true
		}
	}

	return false
}

func parsePluginConfigs(row *core.Record) ([]FTSConfig, error) {
	raw, ok := row.GetRaw(configField).(types.JSONRaw)
	if !ok {
		return nil, errors.New("config field is not json")
	}

	var configs []FTSConfig
	if err := raw.Scan(&configs); err != nil {
		return nil, fmt.Errorf("decode config json: %w", err)
	}

	if len(configs) == 0 {
		return nil, errors.New("config must include at least one entry")
	}

	return configs, nil
}
