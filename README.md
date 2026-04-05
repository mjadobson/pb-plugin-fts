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
   - `allow_snippets`: optional boolean enabling `snippet=` query output; defaults to `false`
   - `allow_highlights`: optional boolean enabling `highlight=` query output; defaults to `false`
   - `min_prefix_query_length`: optional integer threshold for keeping prefix queries like `moon*`; shorter prefixes are automatically downgraded to normal term searches and the default `0` disables prefix behavior entirely
   - `prefixes`: optional array of FTS5 prefix index lengths such as `[2, 3]`; improves prefix-query performance at the cost of index size
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
    "allow_snippets": true,
    "allow_highlights": true,
    "min_prefix_query_length": 3,
    "prefixes": [2, 3],
    "tokenizer": "porter"
  }
]
```

When no `sort` query parameter is provided, the plugin orders results by SQLite FTS5 rank. If `field_weights` is configured, it uses a weighted `bm25(...)` rank so matches in more important fields rise to the top.

Prefix queries like `moon*` are downgraded to normal term searches by default. To allow them efficiently, set `min_prefix_query_length` and configure suitable `prefixes` values for the collection. For example, with `min_prefix_query_length: 3`, `mo*` becomes `mo` while `moon*` remains a prefix query.

You can also request SQLite FTS5 auxiliary output for matched rows:

- `highlight=field1,field2` adds `_fts_highlight_<field>` values to each returned record
- `snippet=field1,field2` adds `_fts_snippet_<field>` values to each returned record
- `highlightBefore` / `highlightAfter` customize `highlight()` markup and default to `<b>` / `</b>`
- `snippetBefore` / `snippetAfter` customize `snippet()` markup and default to `<b>` / `</b>`
- `snippetEllipsis` customizes the snippet truncation marker and defaults to `...`
- `snippetTokens` controls snippet length and must be between `1` and `64` (default `16`)

These auxiliary values are only computed for the paged result set returned by the current search, not for the full collection.
They are disabled by default and must be explicitly enabled per collection with `allow_snippets` and `allow_highlights`.

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
    highlight: "title",
    snippet: "body",
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
    highlight: 'title',
    snippet: 'body',
  );
```
