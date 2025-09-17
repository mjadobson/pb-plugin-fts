package fts

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/search"
	_ "github.com/pocketbuilds/fts/migrations"
	"github.com/pocketbuilds/xpb"
)

func init() {
	xpb.Register(&Plugin{
		DefaultTokenizer: "porter",
	})
}

type Plugin struct {
	DefaultTokenizer string `json:"default_tokenizer"`
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
	app.OnServe().BindFunc(p.setupFtsRoute)
	app.OnRecordValidate("_fts").BindFunc(p.validateFtsFieldsField)
	app.OnRecordValidate("_fts").BindFunc(p.validateFtsTokenizerField)
	app.OnRecordCreate("_fts").BindFunc(p.createFtsTableOnRecordEvent)
	app.OnRecordUpdate("_fts").BindFunc(p.createFtsTableOnRecordEvent)
	app.OnRecordDelete("_fts").BindFunc(p.dropFtsTableOnRecordDelete)
	app.OnCollectionCreate().BindFunc(p.updateFtsCollectionOnCollectionEvent())
	app.OnCollectionUpdate().BindFunc(p.updateFtsCollectionOnCollectionEvent())
	app.OnCollectionDelete().BindFunc(p.updateFtsCollectionOnCollectionEvent())
	app.OnCollectionUpdate().BindFunc(p.updateFtsRecordOnCollectionUpdate())
	app.OnCollectionDelete().BindFunc(p.deleteFtsRecordOnCollectionDelete())
	return nil
}

func (p *Plugin) updateFtsCollectionOnCollectionEvent() func(e *core.CollectionEvent) error {
	return func(e *core.CollectionEvent) error {
		if e.Collection.Name == "_fts" {
			return e.Next()
		}
		if err := e.Next(); err != nil {
			return err
		}
		ftsCollection, err := e.App.FindCollectionByNameOrId("_fts")
		if err != nil {
			e.App.Logger().Error("failed to find _fts collection", "error", err)
			return nil
		}
		field, ok := ftsCollection.Fields.GetByName("collection").(*core.SelectField)
		if field == nil || !ok {
			e.App.Logger().Error("failed to get collection field from _fts", "error", err)
			return nil
		}
		collectionsNameQuery := e.App.DB().Select("name").From("_collections").
			Where(dbx.HashExp{
				"system": false,
			}).
			AndWhere(dbx.Not(dbx.HashExp{
				"name": "_fts",
			})).
			OrderBy("name")
		field.Values = []string{}
		if err := collectionsNameQuery.Column(&field.Values); err != nil {
			e.App.Logger().Error("failed to query collection names", "error", err)
			return nil
		}
		if err := e.App.Save(ftsCollection); err != nil {
			e.App.Logger().Error("failed to update _fts collection", "error", err)
			return nil
		}
		return nil
	}
}

func (p *Plugin) updateFtsRecordOnCollectionUpdate() func(e *core.CollectionEvent) error {
	return func(e *core.CollectionEvent) error {
		if e.Collection.Name == "_fts" {
			return e.Next()
		}
		ftsRecord, err := e.App.FindFirstRecordByData("_fts", "collection", e.Collection.Name)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			return e.Next()
		}
		if err := p.dropFtsTable(e.App, ftsRecord); err != nil {
			return err
		}
		if err := e.Next(); err != nil {
			return err
		}
		var (
			fields    []string
			newFields []string
		)
		if err := ftsRecord.UnmarshalJSONField("fields", &fields); err != nil {
			return err
		}
		validFieldNames := e.Collection.Fields.FieldNames()
		for _, f := range fields {
			if slices.Contains(validFieldNames, f) {
				newFields = append(newFields, f)
			}
		}
		ftsRecord.Set("fields", newFields)
		if err := e.App.Save(ftsRecord); err != nil {
			return err
		}
		return nil
	}
}

func (p *Plugin) deleteFtsRecordOnCollectionDelete() func(e *core.CollectionEvent) error {
	return func(e *core.CollectionEvent) error {
		if e.Collection.Name == "_fts" {
			return e.Next()
		}
		ftsRecord, err := e.App.FindFirstRecordByData("_fts", "collection", e.Collection.Name)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			return e.Next()
		}
		if err := e.App.Delete(ftsRecord); err != nil {
			return err
		}
		return e.Next()
	}
}

func (p *Plugin) createFtsTableOnRecordEvent(e *core.RecordEvent) error {
	originalApp := e.App
	txErr := e.App.RunInTransaction(func(txApp core.App) error {
		e.App = txApp

		if err := e.Next(); err != nil {
			return err
		}

		if err := p.createFtsTable(e.App, e.Record); err != nil {
			return err
		}

		return nil
	})
	e.App = originalApp
	return txErr
}

func (p *Plugin) dropFtsTableOnRecordDelete(e *core.RecordEvent) error {
	originalApp := e.App
	txErr := e.App.RunInTransaction(func(txApp core.App) error {
		e.App = txApp

		if err := e.Next(); err != nil {
			return err
		}

		if err := p.dropFtsTable(e.App, e.Record); err != nil {
			return err
		}

		return nil
	})
	e.App = originalApp
	return txErr
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

func (p *Plugin) createFtsTable(app core.App, record *core.Record) error {
	collectionName := record.GetString("collection")

	ftsTableName := p.ftsTableNameFromCollectionName(collectionName)

	var fieldNames []string
	if err := record.UnmarshalJSONField("fields", &fieldNames); err != nil {
		return err
	}

	ftsFieldNames := p.ftsFieldNameSlice(fieldNames)

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
		record.GetString("tokenizer"),
	))
	if _, err := query.Execute(); err != nil {
		if strings.Contains(err.Error(), "no such tokenizer") {
			return validation.Errors{
				"tokenizer": validation.NewError("invalid_tokenizer", "no such tokenizer"),
			}
		}
		return err
	}

	query = app.DB().NewQuery(fmt.Sprintf(
		`INSERT INTO %s (%s) SELECT %s FROM %s;`,
		ftsTableName,
		strings.Join(ftsFieldNames, ", "),
		strings.Join(fieldNames, ", "),
		collectionName,
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
		collectionName,
		fmt.Sprintf(
			`INSERT INTO %s (%s) SELECT %s FROM %s WHERE id = NEW.id;`,
			ftsTableName,
			strings.Join(ftsFieldNames, ", "),
			strings.Join(fieldNames, ", "),
			collectionName,
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
		collectionName,
		fmt.Sprintf(
			`UPDATE %s SET %s;`,
			ftsTableName,
			func() string {
				results := make([]string, 0, len(ftsFieldNames))
				for i, ftsFieldName := range ftsFieldNames {
					fieldName := fieldNames[i]
					results = append(results, fmt.Sprintf(
						"%s = NEW.%s",
						ftsFieldName, fieldName,
					))
				}
				return strings.Join(results, ", ")
			}(),
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
		collectionName,
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

func (p *Plugin) dropFtsTable(app core.App, record *core.Record) error {
	ftsTableName := p.ftsTableNameFromCollectionName(record.GetString("collection"))
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

func (p *Plugin) validateFtsFieldsField(e *core.RecordEvent) error {
	err := e.Next()
	validationErrs := validation.Errors{}
	if e, ok := err.(validation.Errors); ok {
		validationErrs = e
	} else if err != nil {
		return err
	}
	if _, ok := validationErrs["field"]; ok {
		return validationErrs
	}
	var fields []string
	if err := e.Record.UnmarshalJSONField("fields", &fields); err != nil {
		validationErrs["fields"] = validation.NewError("expected_type_error", "must be an array of strings")
		return validationErrs
	}
	collection, err := e.App.FindCachedCollectionByNameOrId(e.Record.GetString("collection"))
	if err != nil {
		return err
	}
	if len(fields) == 0 {
		for k := range core.NewRecord(collection).PublicExport() {
			if k != core.FieldNameCollectionId && k != core.FieldNameCollectionName {
				fields = append(fields, k)
			}
		}
		e.Record.Set("fields", fields)
	}
	if !slices.Contains(fields, "id") {
		fields = append([]string{"id"}, fields...)
		e.Record.Set("fields", fields)
	}
	missingFields := make([]string, 0, len(fields))
	for _, f := range fields {
		if collection.Fields.GetByName(f) == nil {
			missingFields = append(missingFields, f)
		}
	}
	if len(missingFields) > 0 {
		validationErrs["fields"] = validation.NewError("fields_do_not_exist",
			fmt.Sprintf(
				"the following fields do not exist is collection %s: %s",
				collection.Name, strings.Join(missingFields, ", "),
			),
		)
		return validationErrs
	}
	if collection.IsAuth() {
		illegalFields := make([]string, 0, 2)
		if slices.Contains(fields, core.FieldNamePassword) {
			illegalFields = append(illegalFields, core.FieldNamePassword)
		}
		if slices.Contains(fields, core.FieldNameTokenKey) {
			illegalFields = append(illegalFields, core.FieldNameTokenKey)
		}
		if len(illegalFields) != 0 {
			validationErrs["fields"] = validation.NewError("illegal_fields",
				fmt.Sprintf(
					"the following fields are not allowed: %s",
					strings.Join(illegalFields, ", "),
				),
			)
			return validationErrs
		}
	}
	if len(validationErrs) != 0 {
		return validationErrs
	}
	return nil
}

func (p *Plugin) validateFtsTokenizerField(e *core.RecordEvent) error {
	if e.Record.GetString("tokenizer") == "" {
		e.Record.Set("tokenizer", p.DefaultTokenizer)
	}
	return e.Next()
}
