# Universal parser architecture

The binary has three production adapters over the same parser pipeline. The one-shot job uses an
immutable configuration snapshot:

```text
ENV -> Load/Normalize/Validate -> JobConfig
                                  |
Input discovery -> Stream decoder -> SourceRecord
                                  -> source mapping
                                  -> ordered transformations and conversion
                                  -> record validation
                                  -> canonical Record
                                  -> transactional batch repository
                                  -> database
                         errors -> structured ErrorSink
```

The read-only HTTP adapter uses the same immutable parser snapshot and MinIO
HTTP-gateway convention as `cashrecon-sch-parse-file-atm-bersama`:

```text
ENV -> LoadParser/ValidateParser -> immutable parser engine
POST /parse-file-dynamic -> final_minio_path + final_file_name
                         -> HEAD identity + size
                         -> GET {MINIO_BASE_URL}/{MINIO_BUCKET_NAME}/{path}/{file}
                         -> streamed input -> parser pipeline
                         -> HEAD identity verification -> bounded preview response
```

The gateway base URL, bucket, optional allowed path prefix, parser schema, and resource ceilings
are process ENV and cannot be overridden by callers. Scheduler metadata is accepted for endpoint
compatibility but does not influence parsing. The gateway exposes HTTP Range reads for a future or
external checkpoint orchestrator, but this read-only preview starts at offset zero and does not
persist checkpoint state. This path does not construct a database repository.

The scheduler adapter resolves its configuration per request:

```text
POST /parse-file-dynamic
  -> central ParamParseFile + ParamParseFileMappingColumn (target and mapping)
  -> central Product (target MSSQL connection)
  -> target ParamReadFile + ParamReadFileColumn (source reader rules)
  -> HEAD -> bounded streaming GET -> parser pipeline
  -> transaction + bounded batches -> existing target table
  -> final HEAD identity verification -> COMMIT
```

It repeats database routing because an HTTP service cannot reuse the scheduler process's open DB
connection. It does not delete existing rows and does not provision target tables. Parser rules and
target identifiers are never accepted from the HTTP body.

## Boundaries

- `internal/config` owns ENV decoding, normalization, and fail-fast validation.
- `internal/input` discovers matching regular files deterministically.
- `internal/miniogateway` provides HEAD metadata, validated streaming GET, and offset Range access to the existing HTTP object gateway.
- `internal/preview` owns the read-only HTTP use case and transport boundary.
- `internal/dynamicjob` owns scheduler request validation, MSSQL configuration routing, strict
  per-file orchestration, and its HTTP boundary.
- `internal/parser` owns streaming framing for each supported format.
- `internal/mapping` resolves index, header, JSON, and XML selectors into canonical fields.
- `internal/transformation` changes configured values and performs configured type conversion.
- `internal/validation` applies format-independent record constraints.
- `internal/service` orchestrates files, error policy, transactions, and batching.
- `internal/repository` defines persistence ports; `sqlrepo` implements dynamic SQL safely.
- `internal/model` contains records and stable structured record errors shared across layers.

The older SQL-rule/FSM packages remain isolated as legacy code. Scheduler mode reads only the typed
parameter tables described above and does not route through the legacy HTTP handler.

## Runtime invariants

1. Process ENV is validated before network access. Scheduler reader configuration is loaded and
   validated before opening the MinIO stream or beginning an insert transaction.
2. A parser receives only parser configuration, never database credentials or target mappings.
3. Record and batch memory are bounded by configured byte/cardinality budgets. Random-access
   document decoders also enforce `PARSER_MAX_DOCUMENT_BYTES` and use private temporary files.
4. SQL identifiers are validated and quoted; record values are always bound parameters.
5. `STRICT` is atomic per file. Any rejected record rolls the file transaction back.
6. `PARTIAL` persists each recoverable error and commits the valid records from that file.
7. Structural decoder, I/O, cancellation, error-sink, and database failures are fatal wherever
   those components apply.
8. Input files are processed in lexical order and records preserve source order.
9. Scheduler mode commits only after the second MinIO HEAD confirms the object is unchanged.
10. Scheduler target mapping remains owned by `ParamParseFileMappingColumn`; reader tables cannot
    select arbitrary target tables or target columns.

## Supported universality

The image includes DELIMITED, CSV, TSV, FIXED_WIDTH, JSON, XML, RAW/TEXT, HTML/HTM, PDF, XLS,
XLSX, and SECTIONED_DELIMITED decoders. The last format supports configurable control records and
a different dynamic header per section without business codes in Go. Schema and mapping changes
within these families require only configuration changes. Scanned PDF requires OCR. Other formats
such as DOC/DOCX, Parquet, Avro, Protobuf, archive input, and encryption require an explicit
decoder adapter before their schemas can use the same downstream pipeline.
