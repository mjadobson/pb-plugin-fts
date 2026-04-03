package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	m.Register(func(app core.App) error {
		// Create _plugins table for FTS plugin configuration
		_, err := app.FindCollectionByNameOrId("_plugins")
		if err == nil {
			// _plugins table already exists
			return nil
		}

		// Create _plugins collection
		pluginsCollection := core.NewBaseCollection("_plugins")
		pluginsCollection.Fields.Add(&core.TextField{
			Name:     "plugin_name",
			Required: true,
		})
		pluginsCollection.AddIndex("idx_plugin_name", true, "`plugin_name`", "")
		pluginsCollection.Fields.Add(&core.JSONField{
			Name:     "config",
			Required: true,
		})
		pluginsCollection.Fields.Add(&core.BoolField{
			Name: "enabled",
		})

		if err := app.Save(pluginsCollection); err != nil {
			return err
		}

		return nil
	}, func(app core.App) error {
		// Migration is not reversible - _plugins table may be used by other plugins
		return nil
	})
}
