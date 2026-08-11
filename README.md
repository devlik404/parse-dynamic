# Dynamic Universal File Parser Job

This repository builds one Go job/image that parses multiple generic file families through
environment configuration. Changing a schema, column order, delimiter, type, transformation,
or database mapping does not require rebuilding the source.

Supported decoders:

- `DELIMITED`, including literal multi-character delimiters
- RFC-style `CSV` and `TSV`, including quotes and multiline fields
- `FIXED_WIDTH`, using byte or Unicode-rune positions
- `JSON`: single object, top-level array, NDJSON, and a simple nested record path
- `XML`: streaming record path, nested child paths, and `@attribute` selectors
- `RAW`: one logical record per line

Binary/proprietary formats such as XLSX, PDF, Parquet, Avro, Protobuf, compressed, or encrypted
input still require an appropriate decoder adapter. Their downstream mapping can then reuse this
pipeline.

## Processing model

```text
ENV -> validated immutable JobConfig -> file discovery -> streaming decoder
    -> source mapping -> transformations/type conversion -> validation
    -> canonical record -> DB mapping -> transaction + batches -> database
                                   \-> structured rejected-record JSONL
```

No parser contains business field, table, customer, or filename branches. See
[`docs/architecture.md`](docs/architecture.md) for package boundaries and invariants.

## Quick start

1. Copy `.env.example` and provide the required values through your process manager, container,
   Kubernetes ConfigMap/Secret, or shell environment. The job intentionally reads OS ENV once;
   it does not hot-reload configuration during a file.
2. Ensure the configured target table and columns already exist.
3. Run:

```bash
go run ./cmd/app
```

Or build and run one reusable image:

```bash
docker build -t parser-job:v1.0.0 .
docker run --rm --env-file .env \
  -v "$PWD/input:/data/input:ro" \
  -v "$PWD/errors:/data/errors" \
  parser-job:v1.0.0
```

On success the command prints a JSON summary. Any configuration, structural input, I/O, error
sink, or database failure exits non-zero.

## Core configuration

| ENV | Meaning |
|---|---|
| `INPUT_PATH` | One regular file or a directory; symlinks are rejected |
| `FILE_PATTERN` | Basename-only glob applied inside `INPUT_PATH` |
| `PARSER_FILE_TYPE` | `DELIMITED`, `CSV`, `TSV`, `FIXED_WIDTH`, `JSON`, `XML`, or `RAW` |
| `PARSER_COLUMNS` / `PARSER_TYPES` | Ordered canonical schema and configured data types |
| `PARSER_MAPPING` | Source selector to canonical field |
| `PARSER_MAX_RECORD_BYTES` | Hard byte limit for one logical record (default 1 MiB) |
| `PARSER_MAX_FIELDS` | Field/node cardinality limit per record (default 10,000) |
| `DB_DRIVER` | Compiled adapter: `postgres`, `mysql`, or `sqlserver` |
| `DB_COLUMNS` | Stable target INSERT column order |
| `DB_COLUMN_MAPPING` | `canonical_field:database_column` |
| `DB_BATCH_SIZE` | Application batch size; adapter also enforces dialect parameter limits |
| `DB_BATCH_MAX_BYTES` | Retained-memory budget; flush occurs on count or bytes |
| `ERROR_MODE` | `STRICT` or `PARTIAL` |
| `ERROR_OUTPUT_PATH` | JSONL rejected-record sink; required for `PARTIAL` |

The complete catalog, defaults, optional transforms, and format-specific settings are documented
inline in [`.env.example`](.env.example). Invalid scalar values, duplicate fields, inconsistent
mapping cardinality, fixed-width overlap, unsupported types/drivers, unsafe SQL identifiers, and
path traversal fail together before input processing starts. Password and DSN values are never
included in configuration errors. Safety ceilings are 8 MiB and 100,000 fields/nodes per logical
record; production defaults are the more conservative 1 MiB and 10,000.

### Delimited without a header

```dotenv
PARSER_FILE_TYPE=DELIMITED
PARSER_DELIMITER=|
PARSER_HAS_HEADER=false
PARSER_COLUMNS=transaction_id,transaction_date,bank,amount
PARSER_TYPES=string,date,string,decimal
PARSER_MAPPING=0:transaction_id,1:transaction_date,2:bank,3:amount
PARSER_DATE_FORMAT=20060102
```

Changing to reordered comma data needs only ENV changes:

```dotenv
PARSER_DELIMITER=,
PARSER_COLUMNS=transaction_id,bank,amount,transaction_date
PARSER_TYPES=string,string,decimal,date
PARSER_MAPPING=0:transaction_id,1:bank,2:amount,3:transaction_date
PARSER_DATE_FORMAT=2006-01-02
```

### Header mapping

```dotenv
PARSER_FILE_TYPE=CSV
PARSER_HAS_HEADER=true
PARSER_COLUMNS=transaction_id,transaction_date,bank,amount
PARSER_TYPES=string,date,string,decimal
PARSER_MAPPING=id:transaction_id,date:transaction_date,bank:bank,amount:amount
```

The header is validated before the first record is yielded or inserted.

### Fixed width

Starts in ENV are 1-based. They are normalized once to zero-based offsets internally.

```dotenv
PARSER_FILE_TYPE=FIXED_WIDTH
PARSER_FIXED_WIDTH_UNIT=BYTE
PARSER_FIXED_WIDTH_FIELDS=transaction_id:1:6:string,transaction_date:7:8:date,bank:15:3:string,amount:18:12:decimal
PARSER_DATE_FORMAT=20060102
```

### JSON and XML

```dotenv
PARSER_FILE_TYPE=JSON
PARSER_JSON_MODE=AUTO
PARSER_JSON_MAPPING=transaction.id:transaction_id,transaction.bank:bank,transaction.amount:amount
```

When JSON `PARSER_COLUMNS/PARSER_TYPES` are omitted, mapped native values are preserved instead of
being guessed or converted to strings. Configure them when explicit conversion is required.

```dotenv
PARSER_FILE_TYPE=XML
PARSER_XML_RECORD_PATH=/transactions/transaction
PARSER_XML_MAPPING=id:transaction_id,bank:bank,amount:amount
```

JSON/XML selectors are intentionally simple streaming paths, not arbitrary JSONPath/XPath filters.
Malformed JSON arrays or XML documents are fatal because safe record resynchronization is not
possible. A malformed NDJSON line is recoverable in `PARTIAL` mode. XML comments, processing
instructions, and in-root CDATA are supported; DTD/DOCTYPE and generic declarations are rejected
to keep streaming and entity handling bounded.

## Transformations and types

Supported types are `string`, `integer`, `int64`, exact `decimal`, `float`, `boolean`, `date`,
`datetime`, `uuid`, and `json`. Empty values do not silently become numeric zero.

Convenience settings include trim, null-if-empty, uppercase/lowercase fields, default values, and
required fields. Ordered advanced rules use JSON in ENV:

```dotenv
PARSER_TRANSFORMS_JSON=[{"field":"reference","operation":"REPLACE","from":"-","to":""},{"field":"reference","operation":"SUBSTRING","start":0,"length":12}]
```

Execution order is mapping, convenience transformations, ordered rules, type conversion, then
required-field validation. Decimal values retain exact text/scale and PostgreSQL receives a native
`NUMERIC`; validated UUID values receive the native PostgreSQL UUID type.

## Error and transaction semantics

- `STRICT`: one rejected record stops parsing and rolls back every batch for that input file.
- `PARTIAL`: recoverable record errors are appended to JSONL and valid records before and after
  them are committed.
- Database, I/O, cancellation, structural decoder, and error-sink failures are fatal in both modes.
- Transactions are atomic per file. Previously committed files are not rolled back if a later file
  fails.

`STRICT` requires a transactional target table. For MySQL, use InnoDB (or another engine that
honors transactions); a non-transactional table cannot provide rollback semantics.

The JSONL error schema contains file, logical record number, optional physical line, field, bounded
raw value, stable code, phase, and message. Messages exclude raw values to avoid accidental data
leakage in the process log.

For multiple files, configure a database uniqueness/idempotency key before enabling automatic Job
retries. The supplied Kubernetes manifest sets `backoffLimit: 0` to avoid replaying a previously
committed file by default.

## Verification and deployment

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/app
```

The tests cover all seven formats, reordered headers, quoted/multiline CSV, JSON modes and streaming
paths, XML records, fixed-width boundaries, mappings, transformations, types, batching, exact
decimal/UUID SQL binding, SQL injection-resistant query construction, and STRICT/PARTIAL behavior.
Repository tests exercise SQL construction, native pgx codecs, splitting, and transactional fakes;
run a release smoke test against each real database/version used by your environment because this
repository does not provision those external servers or target tables.

Deployment examples:

- [`Dockerfile`](Dockerfile)
- [`deploy/kubernetes-job.yaml`](deploy/kubernetes-job.yaml)

The older SQL-rule/FSM and HTTP packages remain in the repository as isolated legacy components.
The production `cmd/app` entry point runs only the ENV-configured one-shot universal job.
