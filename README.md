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
- `SECTIONED_DELIMITED`: dynamic section headers such as `RH/SH/SB/SF/RF`

Binary/proprietary formats such as XLSX, PDF, Parquet, Avro, Protobuf, compressed, or encrypted
input still require an appropriate decoder adapter. Their downstream mapping can then reuse this
pipeline.

## Processing model

```text
ENV -> validated immutable JobConfig -> file discovery -> streaming decoder
    -> source mapping -> transformations/type conversion -> validation
    -> canonical record -> DB mapping -> transaction + batches -> database
                                   \-> structured rejected-record JSONL

HTTP request -> validated MinIO path + filename -> existing MinIO HTTP gateway
             -> streamed file -> immutable ENV parser -> bounded JSON preview (no database write)
```

No parser contains business field, table, customer, or filename branches. See
[`docs/architecture.md`](docs/architecture.md) for package boundaries and invariants.

## Quick start

1. Copy `.env.example` and provide the required values through your process manager, container,
   Kubernetes ConfigMap/Secret, or shell environment. For local runs, `.env` is loaded automatically;
   existing OS/Kubernetes variables take precedence. Configuration is not hot-reloaded during a run.
2. In `APP_MODE=JOB`, ensure the configured target table and columns already exist.
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

### Parsing endpoint using the existing MinIO gateway

Run the same binary as a read-only HTTP parser by setting `APP_MODE=HTTP`. This mode follows the
connection contract used by `cashrecon-sch-parse-file-atm-bersama`: the service receives
`final_minio_path` and `final_file_name`, then streams one object from
`GET {MINIO_BASE_URL}/{MINIO_BUCKET_NAME}/{path}/{filename}`. It does not require MinIO/S3 SDK
credentials and does not write a temporary local copy.

```dotenv
APP_MODE=HTTP
HTTP_ADDR=:8080
MINIO_BASE_URL=http://sch-minio.reopsc:8080
MINIO_BUCKET_NAME=rsp-reopsc
MINIO_HTTP_TIMEOUT=5m
# Optional server-side restriction for final_minio_path.
MINIO_PATH_PREFIX=
PREVIEW_REQUIRE_AUTH=true
PREVIEW_BEARER_TOKEN=<generate-a-random-token-of-at-least-32-bytes>
PREVIEW_MAX_INPUT_BYTES=67108864
PREVIEW_REQUEST_TIMEOUT=30s
PREVIEW_MAX_CONCURRENCY=4
```

Set the parser variables in the same process ENV. A ready-to-use example for the dynamic
`RH/SH/SB/SF/RF` format is available at
[examples/cashrecon-sch-parse-file-atm-bersama.env](examples/cashrecon-sch-parse-file-atm-bersama.env).
Then call the reference-compatible route:

```bash
curl --fail-with-body \
  -H "Authorization: Bearer ${PREVIEW_BEARER_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{
    "la_num": 123,
    "final_minio_path": "reconciliation/atm-bersama/20260702",
    "final_file_name": "report-20260702.txt",
    "product_id": "ATM_BERSAMA",
    "task": "PARSE_FILE",
    "activity": "RECONCILIATION",
    "file_date": "20260702",
    "limit": 20
  }' \
  http://localhost:8080/parse-file-dyanmic
```

Only `final_minio_path` and `final_file_name` select the object; the scheduler metadata is accepted
for request compatibility but does not alter parsing. `limit` controls the preview size. Parser
configuration is loaded and validated once from ENV at startup, so callers cannot switch schemas.
`MINIO_PATH_PREFIX`, when non-empty, restricts accessible object paths. The endpoint returns parsed
records and recoverable errors, enforces input/request/value/response/time/concurrency limits, and
never inserts into the database.

Treat this endpoint as an internal service because preview output can contain financial data. Keep
the Kubernetes Service as `ClusterIP`, terminate TLS at a trusted internal gateway, and source the
optional `PREVIEW_BEARER_TOKEN` from a secret manager. Set `PREVIEW_REQUIRE_AUTH=false` only when an
equivalent trusted gateway enforces authentication. The example
[NetworkPolicy](deploy/kubernetes-api.yaml) accepts traffic only from same-namespace pods labeled
`access-universal-parser-api=true`; adapt the selector if the gateway runs in another namespace.
`GET /healthz` is an unauthenticated process-liveness check only. The manifest uses a TCP readiness
probe because it intentionally does not fetch a business file merely to test gateway readiness.

## Core configuration

| ENV | Meaning |
|---|---|
| `INPUT_PATH` | One regular file or a directory; symlinks are rejected |
| `FILE_PATTERN` | Basename-only glob applied inside `INPUT_PATH` |
| `PARSER_FILE_TYPE` | `DELIMITED`, `CSV`, `TSV`, `FIXED_WIDTH`, `JSON`, `XML`, `RAW`, or `SECTIONED_DELIMITED` |
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

### Dynamic headers per section

`SECTIONED_DELIMITED` handles a file where each section declares its own header. It is generic:
the control codes, indexes, and duplicate-name policy all come from configuration.

```dotenv
PARSER_FILE_TYPE=SECTIONED_DELIMITED
PARSER_DELIMITER=|
PARSER_COLUMNS=record_type,section_key,payload
PARSER_TYPES=string,string,json
PARSER_MAPPING=record_type:record_type,section_key:section_key,payload:payload
PARSER_RECORD_TYPE_INDEX=0
PARSER_SECTION_KEY_INDEX=1
PARSER_FILE_HEADER_CODE=RH
PARSER_SECTION_HEADER_CODE=SH
PARSER_DATA_CODE=SB
PARSER_SECTION_FOOTER_CODE=SF
PARSER_FILE_FOOTER_CODE=RF
PARSER_DYNAMIC_HEADER_START_INDEX=2
PARSER_DATA_START_INDEX=2
PARSER_DUPLICATE_HEADER_POLICY=SUFFIX_INDEX
```

Only data records are yielded. Every `SH` supplies the field names for its following `SB`
records. Dynamic fields are returned under `payload` and are also available as flattened source
selectors. Repeated header names receive deterministic suffixes such as `TRF_TRX_BNF__2` when
`SUFFIX_INDEX` is selected. Structural control-record errors remain fatal; safely framed invalid
data rows are returned as structured record errors.

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

The tests cover all eight formats, reordered headers, quoted/multiline CSV, JSON modes and streaming
paths, XML records, fixed-width boundaries, mappings, transformations, types, batching, exact
decimal/UUID SQL binding, SQL injection-resistant query construction, and STRICT/PARTIAL behavior.
Repository tests exercise SQL construction, native pgx codecs, splitting, and transactional fakes;
run a release smoke test against each real database/version used by your environment because this
repository does not provision those external servers or target tables.

Deployment examples:

- [`Dockerfile`](Dockerfile)
- [`deploy/kubernetes-job.yaml`](deploy/kubernetes-job.yaml)
- [`deploy/kubernetes-api.yaml`](deploy/kubernetes-api.yaml)

The older SQL-rule/FSM and HTTP packages remain in the repository as isolated legacy components.
The production `cmd/app` entry point selects the one-shot job or MinIO-backed preview server with
the strict `APP_MODE=JOB|HTTP` setting; parser behavior remains configuration-driven in both modes.
For compatibility, legacy `MODE=HTTP` still starts the server and every other legacy `MODE` value
(including `production`) continues to run the one-shot job. When present, `APP_MODE` takes precedence.
