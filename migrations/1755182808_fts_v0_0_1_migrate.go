package migrations

import (
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	m.Register(func(app core.App) error {
		// add up queries...
		var collectionNames []string
		collectionsNameQuery := app.DB().Select("name").From("_collections").Where(dbx.HashExp{
			"system": false,
		})
		if err := collectionsNameQuery.Column(&collectionNames); err != nil {
			return err
		}

		collection := core.NewBaseCollection("_fts")
		collection.Fields.Add(&core.SelectField{
			Name:      "collection",
			MaxSelect: 1,
			Required:  true,
			Values:    collectionNames,
		})
		collection.AddIndex("idx_k548G4ctcg", true, "`collection`", "")
		collection.Fields.Add(&core.JSONField{
			Name: "fields",
		})
		collection.Fields.Add(&core.TextField{
			Name: "tokenizer",
		})
		if err := app.Save(collection); err != nil {
			return err
		}

		return nil
	}, func(app core.App) error {
		// add down queries...
		collection, err := app.FindCollectionByNameOrId("_fts")
		if err != nil {
			return err
		}
		if err := app.Delete(collection); err != nil {
			return err
		}
		return nil
	})
}
