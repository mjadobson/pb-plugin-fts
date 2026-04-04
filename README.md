# Full Text Search

An [xpb](https://github.com/pocketbuilds/xpb) plugin for [Pocketbase](https://pocketbase.io/) that allows for configurable [sqlite full text search](https://sqlite.org/fts5.html).

Provides an api endpoint to do full text searches.

## Fork Notes

This repository was forked from [`github.com/pocketbuilds/fts`](https://github.com/pocketbuilds/fts).

The main differences in this fork are:

- configuration now lives in the shared `_plugins` collection instead of a dedicated `_fts` collection
- the plugin no longer uses `pocketbuilds.toml` configuration; all runtime setup is done through `_plugins`
- config validation normalizes defaults in `_plugins`, including automatically adding the `id` field and defaulting the tokenizer to `porter`
- only one FTS config per collection is supported, matching the single generated FTS table per collection

## Installation

1. [Install XPB](https://docs.pocketbuilds.com/installing-xpb).
2. [Use the builder](https://docs.pocketbuilds.com/using-the-builder):

```sh
xpb build --with github.com/mjadobson/pb-plugin-fts@latest
```

## Setup

1. Launch PocketBase with the plugin installed. It will create a shared `_plugins` collection if needed.
2. Add a row to `_plugins` with `plugin_name = "fts"` and `enabled = true`.
3. Set `_plugins.config` to a JSON array of collection configs. Each config entry accepts:
   - `collection_name`: the collection to index
   - `fields`: optional array of field names to index; if omitted or empty, all public fields are indexed
   - `field_weights`: optional object mapping field names to ranking weights when using default FTS sort; omitted fields default to `1`
   - `tokenizer`: optional [fts5 tokenizer](https://sqlite.org/fts5.html#tokenizers); defaults to `porter`
4. The `id` field is always added automatically with a default weight of `1`, and only one FTS config per collection is supported.
5. Use the FTS API endpoint to access full text search capabilities:

```
GET /api/collections/{collection}/records/fts
```

Example `_plugins.config` value:

```json
[
  {
    "collection_name": "news",
    "fields": ["title", "body"],
    "field_weights": {
      "title": 10,
      "body": 1
    },
    "tokenizer": "porter"
  }
]
```

When no `sort` query parameter is provided, the plugin orders results by SQLite FTS5 rank. If `field_weights` is configured, it uses a weighted `bm25(...)` rank so matches in more important fields rise to the top.

## Using the FTS Api Endpoint

### JavaScript SDK

```js
// One-Off use of endpoint<script type="module">
const pb = new PocketBase("https://example.com");
const collectionName = "news";

const result = await pb.send(`/api/collections/${collectionName}/records/fts`, {
  query: {
    page: 1,
    perPage: 20,
    sort: "+created", // keep blank to use sort by fts rank
    filter: 'status = true && created > "2022-08-01 10:00:00"',
    // Use MATCH value
    // https://sqlite.org/fts5.html
    search: "ghosts OR aliens",
  },
});
```

```js
// Use beforeSend hook to always use fts route on record list
  const pb = new PocketBase("https://example.com")

  const re = new RegExp("/api/collections/[^/]+/records");
  pb.beforeSend = (url, options) => {
  	if (re.test(url) && options.method === "GET") {
  		url += "/fts";
  	}
  	return { url, options };
  }

  const result = await pb.collection("news").getList(1, 20, {    {
    page: 1,
    perPage: 20,
    sort: '+created', // keep blank to use sort by fts rank
    filter: 'status = true && created > "2022-08-01 10:00:00"',
    // Use MATCH value
    // https://sqlite.org/fts5.html
    search: 'ghosts OR aliens', // leave blank if fts is not enabled on collection
  );
```
