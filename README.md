# Full Text Search

An [xpb](https://github.com/pocketbuilds/xpb) plugin for [Pocketbase](https://pocketbase.io/) that allows for configurable [sqlite full text search](https://sqlite.org/fts5.html).

Provides an api endpoint to do full text searches.

## Installation

1. [Install XPB](https://docs.pocketbuilds.com/installing-xpb).
2. [Use the builder](https://docs.pocketbuilds.com/using-the-builder):

```sh
xpb build --with github.com/pocketbuilds/fts@latest
```

## Setup
1. Launch Pocketbase with plugin installed. You will notice it added a collection named `_fts`.
2. Create and `_fts` record to enable full text search for the chosen collection.
3. Optionally, choose only certain fields to be used in the vocabulary using the `_fts.fields` JSON array field of field names. If left `null` it will be populated with all collection fields. The id field is required, but will be automatically added if missing.
4. Optionally, change the [tokenizer](https://sqlite.org/fts5.html#tokenizers) that will be used. The default tokenizer without changing the plugin config is `porter`.
5. Use the fts api endpoint to access full text search capabilities:
```
GET /api/collections/{collection}/records/fts
```

## Plugin Config

```toml
# pocketbuilds.toml

[fts]
# String default tokenizer to use for full text search
#  virtual tables. Can be changed individually in the
#  _fts record.
#  - default: "porter"
default_tokenizer = "porter"
```

## Using the FTS Api Endpoint

### JavaScript SDK

```js
// One-Off use of endpoint<script type="module">
  const pb = new PocketBase("https://example.com")
  const collectionName = "news";

  const result = await pb.send(
    `/api/collections/${collectionName}/records/fts`,
    {
      query: {
        page: 1,
        perPage: 20,
        sort: '+created', // keep blank to use sort by fts rank
        filter: 'status = true && created > "2022-08-01 10:00:00"',
        // Use MATCH value
        // https://sqlite.org/fts5.html
        search: 'ghosts OR aliens',
      },
    },
  );
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


