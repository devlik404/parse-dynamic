# Universal parser architecture

The production path is a one-shot job with an immutable configuration snapshot:

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

## Boundaries

- `internal/config` owns ENV decoding, normalization, and fail-fast validation.
- `internal/input` discovers matching regular files deterministically.
- `internal/parser` owns streaming framing for each supported format.
- `internal/mapping` resolves index, header, JSON, and XML selectors into canonical fields.
- `internal/transformation` changes configured values and performs configured type conversion.
- `internal/validation` applies format-independent record constraints.
- `internal/service` orchestrates files, error policy, transactions, and batching.
- `internal/repository` defines persistence ports; `sqlrepo` implements dynamic SQL safely.
- `internal/model` contains records and stable structured record errors shared across layers.

The older SQL-rule/FSM packages remain isolated as legacy code. The universal job does not load
business parsing rules from SQL Server and does not route through the legacy HTTP handler.

## Runtime invariants

1. Configuration is validated before input discovery or database connection.
2. A parser receives only parser configuration, never database credentials or target mappings.
3. Memory is bounded by record bytes/cardinality plus the configured batch byte budget.
4. SQL identifiers are validated and quoted; record values are always bound parameters.
5. `STRICT` is atomic per file. Any rejected record rolls the file transaction back.
6. `PARTIAL` persists each recoverable error and commits the valid records from that file.
7. Structural decoder, I/O, cancellation, error-sink, and database failures are fatal in both modes.
8. Input files are processed in lexical order and records preserve source order.

## Supported universality

The image includes DELIMITED, CSV, TSV, FIXED_WIDTH, JSON, XML, and RAW decoders. Schema and
mapping changes within these families require only ENV changes. Proprietary or binary formats
such as XLSX, PDF, Parquet, Avro, Protobuf, compression, and encryption require an explicit
decoder adapter before their schemas can use the same downstream pipeline.
