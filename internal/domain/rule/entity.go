package rule

/*
====================================================
RULE VERSION (ROOT AGGREGATE)
====================================================

RuleVersion adalah snapshot rule parsing
yang di-load dari ParamDynamic (DB)
dan digunakan secara immutable oleh engine.
*/

type RuleVersion struct {
	// Logical rule identifier supplied by the configured legacy rule source.
	ID string

	// Daftar section dalam file (urutan penting)
	Sections []SectionRule
}

/*
====================================================
SECTION RULE
====================================================

SectionRule mendeskripsikan satu bagian file.
Contoh:
- HEADER
- DATA
- SUBTOTAL
- TOTAL
*/

type SectionRule struct {
	// Nama section (untuk log & audit)
	Name string

	// Regex untuk mendeteksi baris masuk section
	// Contoh: ^[0-9]{6}\s
	StartPattern string

	// Regex untuk mendeteksi akhir section (opsional)
	// Jika kosong → section berhenti saat section lain match
	EndPattern string

	// Aturan parsing baris di section ini
	Record RecordRule
}

/*
====================================================
RECORD RULE
====================================================

RecordRule menentukan bagaimana satu baris
di-parse menjadi field-field terstruktur.
*/

type RecordRule struct {
	// Jenis record:
	// - "delimited"
	// - "fixed_width"
	Type string

	// Delimiter (khusus delimited)
	// Contoh: "|", ",", ";", "\t"
	Delimiter string

	// Definisi field-field
	Fields []FieldRule

	// Jika true → baris di-ignore
	// (header, footer, separator, dll)
	Ignore bool
}

/*
====================================================
FIELD RULE
====================================================

FieldRule mendeskripsikan satu kolom data.
*/

type FieldRule struct {
	// Nama field (akan jadi key output)
	Name string

	// ===== DELIMITED =====
	// Posisi kolom (0-based)
	Index int

	// ===== FIXED WIDTH =====
	// Posisi awal (0-based)
	Start int

	// Panjang field
	Length int

	// Tipe data field:
	// - string
	// - integer
	// - decimal
	// - date
	DataType string
}
