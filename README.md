# Job Parser File Universal Dinamis

Repository ini menghasilkan satu job/image Go yang dapat mem-parsing berbagai kelompok format file
melalui konfigurasi environment. Perubahan skema, urutan kolom, delimiter, tipe data, transformasi,
atau mapping database tidak memerlukan build ulang source code.

Decoder yang didukung:

- `DELIMITED`, termasuk delimiter literal dengan lebih dari satu karakter
- `CSV` dan `TSV` bergaya RFC, termasuk quoted field dan field multiline
- `FIXED_WIDTH`, menggunakan posisi byte atau Unicode rune
- `JSON`: object tunggal, array level teratas, NDJSON, dan record path bertingkat sederhana
- `XML`: record path streaming, child path bertingkat, dan selector `@attribute`
- `RAW`: satu record logis per baris
- `TEXT`: alias `RAW` untuk input plain text
- `HTML` / `HTM`: baris dari indeks tabel HTML zero-based yang dapat dikonfigurasi
- `PDF`: satu record logis per halaman dari text layer yang tertanam
- `XLS` dan `XLSX`: baris dari worksheet yang dipilih berdasarkan nama atau indeks zero-based
- `SECTIONED_DELIMITED`: header section dinamis seperti `RH/SH/SB/SF/RF`

PDF hasil scan atau yang hanya berisi gambar membutuhkan OCR, yang sengaja tidak disertakan dalam
decoder PDF. Format biner/proprietary lain seperti DOC/DOCX, Parquet, Avro, Protobuf, input archive,
atau input terenkripsi tetap membutuhkan adapter decoder yang sesuai. Setelah itu, mapping ke tahap
berikutnya dapat menggunakan kembali pipeline ini.

## Model pemrosesan

```text
ENV -> JobConfig immutable tervalidasi -> penemuan file -> decoder streaming
    -> mapping sumber -> transformasi/konversi tipe -> validasi
    -> record canonical -> mapping DB -> transaksi + batch -> database
                                   \-> JSONL record gagal yang terstruktur

Request HTTP -> path + nama file MinIO tervalidasi -> HTTP gateway MinIO yang tersedia
             -> HEAD identitas/ukuran -> GET stream -> parser ENV immutable
             -> verifikasi identitas dengan HEAD -> preview JSON terbatas (tanpa write DB)

Request scheduler -> lookup target ParamParseFile lama -> routing Product DB
                  -> ParamReadFile* di target DB -> HEAD -> streaming GET
                  -> parser dinamis -> batch transactional -> target table yang tersedia
                  -> verifikasi identitas dengan HEAD terakhir -> COMMIT
```

Tidak ada parser yang memiliki percabangan berdasarkan field bisnis, tabel, customer, atau nama file.
Lihat [`docs/architecture.md`](docs/architecture.md) untuk boundary package dan invariant sistem.

## Memulai dengan cepat

1. Salin `.env.example`, lalu isi nilai yang diperlukan melalui process manager, container,
   Kubernetes ConfigMap/Secret, atau shell environment. Untuk eksekusi lokal, `.env` dimuat otomatis;
   variable OS/Kubernetes yang sudah ada memiliki prioritas. Konfigurasi tidak di-hot-reload saat proses berjalan.
2. Pada `APP_MODE=JOB`, pastikan target table dan seluruh kolom yang dikonfigurasi sudah tersedia.
3. Jalankan:

```bash
go run ./cmd/app
```

Atau build dan jalankan satu image yang dapat digunakan ulang:

```bash
docker build -t parser-job:v1.0.0 .
docker run --rm --env-file .env \
  -v "$PWD/input:/data/input:ro" \
  -v "$PWD/errors:/data/errors" \
  parser-job:v1.0.0
```

Jika berhasil, perintah akan mencetak ringkasan JSON. Kegagalan konfigurasi, struktur input, I/O,
error sink, atau database akan menghentikan proses dengan exit code non-zero.

### Endpoint parsing melalui gateway MinIO yang tersedia

Jalankan binary yang sama sebagai parser HTTP read-only dengan `APP_MODE=HTTP`. Flow pembacaan MinIO
mengikuti `cashrecon-sch-parse-file-qris-tap`: service menerima `final_minio_path` dan
`final_file_name`, membaca `ETag`, optional `x-amz-version-id`, dan `Content-Length` melalui `HEAD`,
lalu melakukan streaming body melalui
`GET {MINIO_BASE_URL}/{MINIO_BUCKET_NAME}/{path}/{filename}`. Setelah file selesai dibaca, service
mengirim `HEAD` kedua dan menolak hasil dengan `409 OBJECT_CHANGED` jika identitas atau ukurannya
berubah. Object yang lebih besar dari `PREVIEW_MAX_INPUT_BYTES` ditolak sebelum request `GET`.

Gateway juga mendukung `OpenObject(..., offset)` melalui `Range: bytes=<offset>-` dan mensyaratkan
`206 Partial Content`, sesuai kontrak resume transport pada job referensi. Endpoint preview selalu
dimulai dari offset nol dan tidak menyimpan checkpoint. Request HTTP yang gagal harus diulang oleh
caller. Decoder streaming tidak membuat salinan lokal; PDF/XLS/XLSX memakai temporary spool private
yang dibatasi karena format tersebut membutuhkan random access. Mode HTTP gateway ini tidak
memerlukan credential SDK MinIO/S3.

```dotenv
APP_MODE=HTTP
HTTP_ADDR=:8080
LOG_LEVEL=info
LOG_FORMAT=text
MINIO_BASE_URL=http://sch-minio.reopsc:8080
MINIO_BUCKET_NAME=rsp-reopsc
MINIO_HTTP_TIMEOUT=5m
# Pembatasan optional pada sisi server untuk final_minio_path.
MINIO_PATH_PREFIX=
PREVIEW_REQUIRE_AUTH=true
PREVIEW_BEARER_TOKEN=<buat-token-acak-minimal-32-byte>
PREVIEW_MAX_INPUT_BYTES=67108864
PREVIEW_REQUEST_TIMEOUT=30s
PREVIEW_MAX_CONCURRENCY=4
```

Atur variable parser pada ENV proses yang sama. Contoh siap pakai untuk format dinamis
`RH/SH/SB/SF/RF` tersedia di
[examples/cashrecon-sch-parse-file-atm-bersama.env](examples/cashrecon-sch-parse-file-atm-bersama.env).
Kemudian panggil route yang kompatibel dengan job referensi:

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
  http://localhost:8080/parse-file-dynamic
```

Hanya `final_minio_path` dan `final_file_name` yang memilih object. Metadata scheduler diterima untuk
kompatibilitas request, tetapi tidak mengubah cara parsing. `limit` mengatur ukuran preview.
Konfigurasi parser dimuat dan divalidasi satu kali dari ENV saat startup sehingga caller tidak dapat
mengganti skema. Jika diisi, `MINIO_PATH_PREFIX` membatasi path object yang dapat diakses. Endpoint
mengembalikan record hasil parsing dan error yang dapat dipulihkan, menerapkan batas input, request,
nilai, respons, waktu, serta concurrency, dan tidak pernah melakukan insert ke database.

Log operasional diterbitkan selama request berjalan: server mulai, pembacaan metadata object,
pembukaan stream object, parser mulai, progress terbatas, verifikasi identitas terakhir, parser
selesai/gagal, dan request HTTP selesai. Setiap request memperoleh header respons `X-Request-ID` dan
ID yang sama muncul pada seluruh baris log terkait. `LOG_FORMAT` dapat berupa `text` (praktis untuk
lokal) atau `json` (disarankan untuk Kubernetes); `LOG_LEVEL` menerima `debug`, `info`, `warn`, atau
`error`. Log sengaja tidak mencantumkan path object, nama file, request body, nilai record,
credential, maupun detail string error dari dependency.

Perlakukan endpoint ini sebagai service internal karena output preview dapat berisi data finansial.
Pertahankan Kubernetes Service sebagai `ClusterIP`, terminasi TLS pada internal gateway tepercaya,
dan ambil optional `PREVIEW_BEARER_TOKEN` dari secret manager. Gunakan
`PREVIEW_REQUIRE_AUTH=false` hanya jika gateway tepercaya yang setara sudah menerapkan autentikasi.
Contoh [NetworkPolicy](deploy/kubernetes-api.yaml) hanya menerima traffic dari pod dalam namespace
yang sama dengan label `access-universal-parser-api=true`; sesuaikan selector jika gateway berjalan
di namespace lain. `GET /healthz` hanya merupakan pemeriksaan process-liveness tanpa autentikasi.
Manifest menggunakan TCP readiness probe agar tidak perlu mengambil file bisnis hanya untuk menguji
kesiapan gateway.

### Parsing dinamis yang dipanggil scheduler

Gunakan `APP_MODE=SCHEDULER` untuk endpoint yang dipanggil oleh `sch-parse-file`. Berbeda dari mode
preview, mode ini memuat parameter parser pada setiap request dan menulis record hasil parsing ke
MSSQL. Buat tabel parameter reader pada setiap database tujuan menggunakan
[`scripts/mssql/create_param_read_file.sql`](scripts/mssql/create_param_read_file.sql).

Kepemilikan konfigurasi sengaja dipisahkan:

- `ParamEngine.dbo.ParamParseFile` di database central menyediakan `TargetDB`, `TargetTable`,
  `FieldFilename`, dan `FieldFiledate`.
- `ParamEngine.dbo.ParamParseFileMappingColumn` di database central menyediakan mapping kolom target
  yang sudah tersedia.
- `ReconConfig.dbo.Product` di database central menentukan host, user, password terdekripsi, dan
  database target.
- `dbo.ParamReadFile` dan `dbo.ParamReadFileColumn` di database target hanya menyediakan format file,
  selector sumber, tipe data, transformasi, dan batas parser.

Karena itu, `DB_COLUMNS` dan `DB_COLUMN_MAPPING` diabaikan pada mode ini. Service memvalidasi bahwa
seluruh kolom tujuan yang dikonfigurasi sudah tersedia. Service tidak pernah membuat/mengubah tabel
bisnis dan tidak menghapus data lama; flow scheduler yang tersedia melakukan delete sesuai scope
sebelum memanggil endpoint ini.

Request dan respons sukses kompatibel dengan adapter scheduler:

```json
{
  "final_file_name": "report-20260829.xlsx",
  "final_minio_path": "reconciliation/product/20260829",
  "file_date": "2026-08-29",
  "la_num": 123,
  "product_id": "PRODUCT_ID",
  "task": "PARSE_FILE",
  "activity": "RECONCILIATION"
}
```

```json
{"status":true,"message":"","total_data":1250}
```

File MinIO tidak di-download penuh ke memory atau file lokal permanen. Flow-nya adalah `HEAD` untuk
ukuran/identitas, streaming `GET`, lalu `HEAD` kedua sebelum commit. Hanya PDF/XLS/XLSX yang memakai
temporary spool terbatas karena library format tersebut membutuhkan random access. Parse error,
error database, stream terpotong, atau perubahan identitas object akan me-rollback seluruh transaksi file.

Minimal, mode scheduler membutuhkan `MINIO_BASE_URL`, `MINIO_BUCKET_NAME`, `SS_RC_HOST`, `SS_RC_PORT`,
`SS_RC_USER`, `SS_RC_PASSWORD`, `SS_RC_DB_PARAM_ENGINE`, `SS_RC_DB_RECON_CONFIG`, `SCH_ENC_KEY`, dan
`SS_TR_PORT`. Lihat [`.env.example`](.env.example) dan
[`deploy/kubernetes-scheduler.yaml`](deploy/kubernetes-scheduler.yaml) untuk contoh runtime lengkap.
Manifest mengharapkan Secret `universal-parser-scheduler-secrets` yang dikelola secara eksternal dan
berisi `SS_RC_USER`, `SS_RC_PASSWORD`, serta `SCH_ENC_KEY`.

## Konfigurasi utama

| ENV | Keterangan |
|---|---|
| `INPUT_PATH` | Satu regular file atau direktori; symlink ditolak |
| `FILE_PATTERN` | Glob basename-only yang diterapkan di dalam `INPUT_PATH` |
| `PARSER_FILE_TYPE` | `DELIMITED`, `CSV`, `TSV`, `FIXED_WIDTH`, `JSON`, `XML`, `RAW`, `TEXT`, `HTML`, `HTM`, `PDF`, `XLS`, `XLSX`, atau `SECTIONED_DELIMITED` |
| `PARSER_COLUMNS` / `PARSER_TYPES` | Skema canonical terurut dan tipe data yang dikonfigurasi |
| `PARSER_MAPPING` | Mapping selector sumber ke field canonical |
| `PARSER_MAX_RECORD_BYTES` | Batas byte mutlak untuk satu record logis (default 1 MiB) |
| `PARSER_MAX_DOCUMENT_BYTES` | Batas input/unpacked document untuk PDF/XLS/XLSX (default 64 MiB) |
| `PARSER_MAX_FIELDS` | Batas jumlah field/node per record (default 10.000) |
| `PARSER_SPREADSHEET_SHEET` | Nama worksheet XLS/XLSX atau indeks zero-based (default `0`) |
| `PARSER_HTML_TABLE_INDEX` | Indeks tabel HTML zero-based (default `0`) |
| `DB_DRIVER` | Adapter yang tersedia: `postgres`, `mysql`, atau `sqlserver` |
| `DB_COLUMNS` | Urutan kolom target `INSERT` yang stabil |
| `DB_COLUMN_MAPPING` | `canonical_field:database_column` |
| `DB_BATCH_SIZE` | Ukuran batch aplikasi; adapter juga menerapkan batas parameter dialect |
| `DB_BATCH_MAX_BYTES` | Batas memory yang ditahan; flush dilakukan berdasarkan jumlah atau byte |
| `ERROR_MODE` | `STRICT` atau `PARTIAL` |
| `ERROR_OUTPUT_PATH` | Tujuan JSONL untuk record yang ditolak; wajib pada mode `PARTIAL` |

Daftar lengkap, nilai default, transformasi optional, dan pengaturan khusus setiap format
didokumentasikan langsung dalam [`.env.example`](.env.example). Nilai scalar yang tidak valid,
field duplikat, jumlah mapping yang tidak konsisten, fixed-width yang overlap, tipe/driver yang tidak
didukung, identifier SQL yang tidak aman, dan path traversal akan dilaporkan bersama sebelum
pemrosesan input dimulai. Nilai password dan DSN tidak pernah disertakan dalam error konfigurasi.
Batas keamanan maksimal adalah 8 MiB dan 100.000 field/node per record logis; default production
yang digunakan lebih konservatif, yaitu 1 MiB dan 10.000.

### Delimited tanpa header

```dotenv
PARSER_FILE_TYPE=DELIMITED
PARSER_DELIMITER=|
PARSER_HAS_HEADER=false
PARSER_COLUMNS=transaction_id,transaction_date,bank,amount
PARSER_TYPES=string,date,string,decimal
PARSER_MAPPING=0:transaction_id,1:transaction_date,2:bank,3:amount
PARSER_DATE_FORMAT=20060102
```

Perubahan menjadi data dengan delimiter koma dan urutan berbeda hanya membutuhkan perubahan ENV:

```dotenv
PARSER_DELIMITER=,
PARSER_COLUMNS=transaction_id,bank,amount,transaction_date
PARSER_TYPES=string,string,decimal,date
PARSER_MAPPING=0:transaction_id,1:bank,2:amount,3:transaction_date
PARSER_DATE_FORMAT=2006-01-02
```

### Mapping berdasarkan header

```dotenv
PARSER_FILE_TYPE=CSV
PARSER_HAS_HEADER=true
PARSER_COLUMNS=transaction_id,transaction_date,bank,amount
PARSER_TYPES=string,date,string,decimal
PARSER_MAPPING=id:transaction_id,date:transaction_date,bank:bank,amount:amount
```

Header divalidasi sebelum record pertama dihasilkan atau dimasukkan ke database.

### Header dinamis per section

`SECTIONED_DELIMITED` menangani file yang setiap section-nya memiliki header sendiri. Implementasinya
bersifat generic: control code, indeks, dan kebijakan nama duplikat seluruhnya berasal dari konfigurasi.

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

Hanya data record yang dihasilkan. Setiap `SH` menyediakan nama field untuk record `SB` setelahnya.
Field dinamis dikembalikan di dalam `payload` dan juga tersedia sebagai selector sumber yang sudah
di-flatten. Nama header yang berulang memperoleh suffix deterministik seperti `TRF_TRX_BNF__2` ketika
`SUFFIX_INDEX` dipilih. Error struktur pada control record tetap fatal; baris data invalid yang
framing-nya masih aman dikembalikan sebagai record error terstruktur.

### Lebar tetap (fixed width)

Posisi awal pada ENV menggunakan basis 1. Secara internal, nilai tersebut dinormalisasi satu kali
menjadi offset berbasis 0.

```dotenv
PARSER_FILE_TYPE=FIXED_WIDTH
PARSER_FIXED_WIDTH_UNIT=BYTE
PARSER_FIXED_WIDTH_FIELDS=transaction_id:1:6:string,transaction_date:7:8:date,bank:15:3:string,amount:18:12:decimal
PARSER_DATE_FORMAT=20060102
```

### JSON dan XML

```dotenv
PARSER_FILE_TYPE=JSON
PARSER_JSON_MODE=AUTO
PARSER_JSON_MAPPING=transaction.id:transaction_id,transaction.bank:bank,transaction.amount:amount
```

Jika `PARSER_COLUMNS/PARSER_TYPES` pada JSON tidak diisi, nilai native hasil mapping dipertahankan,
bukan ditebak atau dikonversi menjadi string. Isi konfigurasi tersebut jika dibutuhkan konversi eksplisit.

```dotenv
PARSER_FILE_TYPE=XML
PARSER_XML_RECORD_PATH=/transactions/transaction
PARSER_XML_MAPPING=id:transaction_id,bank:bank,amount:amount
```

Selector JSON/XML sengaja dibatasi pada streaming path sederhana, bukan filter JSONPath/XPath bebas.
Array JSON atau dokumen XML yang malformed bersifat fatal karena sinkronisasi ulang record secara
aman tidak memungkinkan. Baris NDJSON yang malformed dapat dipulihkan pada mode `PARTIAL`. Comment
XML, processing instruction, dan CDATA di dalam root didukung; DTD/DOCTYPE serta declaration generic
ditolak agar penggunaan streaming dan penanganan entity tetap terbatas.

### Teks, HTML, PDF, dan Excel

Plain text dapat memakai `RAW` atau alias-nya, `TEXT`. HTML menghasilkan satu record untuk setiap
baris pada tabel yang dipilih. HTML, XLS, dan XLSX menggunakan perilaku mapping indeks/header yang
sama dengan format berbasis baris lainnya:

```dotenv
PARSER_FILE_TYPE=XLSX
PARSER_SPREADSHEET_SHEET=Transactions
PARSER_HAS_HEADER=true
PARSER_COLUMNS=transaction_id,bank,amount
PARSER_TYPES=string,string,decimal
PARSER_MAPPING=ID:transaction_id,BANK:bank,AMOUNT:amount
```

Untuk HTML, pilih tabel dengan `PARSER_HTML_TABLE_INDEX=0`. Untuk XLS/XLSX,
`PARSER_SPREADSHEET_SHEET` menerima nama worksheet atau indeks zero-based. Cell kosong di bagian
akhir baris spreadsheet ditambahkan sampai sesuai jumlah kolom konfigurasi/header.

PDF menghasilkan satu record per halaman. Selector sumber yang tersedia adalah `page`, `text`,
`raw`, dan `value`:

```dotenv
PARSER_FILE_TYPE=PDF
PARSER_COLUMNS=page_number,page_text
PARSER_TYPES=int64,string
PARSER_MAPPING=page:page_number,text:page_text
```

Hanya text layer PDF yang diekstrak. Halaman hasil scan atau yang hanya berisi gambar dikembalikan
sebagai error `PDF_PAGE_HAS_NO_TEXT` yang dapat dipulihkan dan membutuhkan adapter OCR terpisah.
PDF, XLS, dan XLSX disalin ke temporary file private yang ukurannya dibatasi karena format tersebut
membutuhkan random access; file dihapus setelah parsing. Dokumen terenkripsi ditolak.

## Transformasi dan tipe data

Tipe yang didukung adalah `string`, `integer`, `int64`, `decimal` presisi exact, `float`, `boolean`,
`date`, `datetime`, `uuid`, dan `json`. Nilai kosong tidak otomatis diubah menjadi angka nol.

Pengaturan praktis mencakup trim, null-if-empty, field uppercase/lowercase, default value, dan
required field. Rule lanjutan yang berurutan menggunakan JSON pada ENV:

```dotenv
PARSER_TRANSFORMS_JSON=[{"field":"reference","operation":"REPLACE","from":"-","to":""},{"field":"reference","operation":"SUBSTRING","start":0,"length":12}]
```

Urutan eksekusinya adalah mapping, transformasi praktis, rule berurutan, konversi tipe, kemudian
validasi required field. Nilai decimal mempertahankan text/scale secara exact dan PostgreSQL menerima
tipe native `NUMERIC`; UUID yang sudah divalidasi menerima tipe native UUID PostgreSQL.

## Semantik error dan transaksi

- `STRICT`: satu record yang ditolak menghentikan parsing dan me-rollback seluruh batch untuk file input tersebut.
- `PARTIAL`: record error yang dapat dipulihkan ditambahkan ke JSONL, sedangkan record valid sebelum
  dan sesudahnya tetap di-commit.
- Kegagalan database, I/O, cancellation, struktur decoder, dan error sink bersifat fatal pada mode
  yang menggunakan komponen tersebut.
- Transaksi bersifat atomic per file. File yang sudah di-commit tidak di-rollback jika file berikutnya gagal.

Mode `STRICT` membutuhkan target table yang mendukung transaksi. Untuk MySQL, gunakan InnoDB atau
engine lain yang menjalankan transaksi; tabel non-transactional tidak dapat memberikan semantik rollback.

Skema error JSONL berisi file, nomor record logis, optional physical line, field, raw value terbatas,
stable code, phase, dan message. Message tidak menyertakan raw value untuk mencegah kebocoran data
secara tidak sengaja pada log proses.

Untuk pemrosesan beberapa file, konfigurasi uniqueness/idempotency key pada database sebelum
mengaktifkan retry Job otomatis. Manifest Kubernetes yang disediakan menggunakan `backoffLimit: 0`
agar secara default tidak memproses ulang file yang sebelumnya sudah di-commit.

## Verifikasi dan penerapan (deployment)

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/app
```

Test mencakup seluruh format yang didukung, header dengan urutan berbeda, CSV quoted/multiline, mode
dan streaming path JSON, record XML, boundary fixed-width, mapping, transformasi, tipe data, batching,
SQL binding decimal/UUID secara exact, konstruksi query yang tahan SQL injection, serta perilaku
`STRICT`/`PARTIAL`. Test repository mencakup konstruksi SQL, native codec pgx, pemecahan batch, dan
transactional fake. Jalankan release smoke test terhadap setiap database/version yang benar-benar
digunakan karena repository ini tidak menyediakan server eksternal atau target table tersebut.

Contoh penerapan:

- [`Dockerfile`](Dockerfile)
- [`deploy/kubernetes-job.yaml`](deploy/kubernetes-job.yaml)
- [`deploy/kubernetes-api.yaml`](deploy/kubernetes-api.yaml)
- [`deploy/kubernetes-scheduler.yaml`](deploy/kubernetes-scheduler.yaml)

Package SQL-rule/FSM dan HTTP lama tetap berada di repository sebagai komponen legacy yang terisolasi.
Entry point production `cmd/app` memilih one-shot job, preview server berbasis MinIO, atau job MSSQL
yang dipanggil scheduler melalui nilai ketat `APP_MODE=JOB|HTTP|SCHEDULER`. Untuk kompatibilitas,
legacy `MODE=HTTP` tetap menjalankan server dan seluruh nilai legacy `MODE` lainnya, termasuk
`production`, tetap menjalankan one-shot job. Jika tersedia, `APP_MODE` memiliki prioritas.
