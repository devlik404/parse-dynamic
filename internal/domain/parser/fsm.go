package parser

/*
FSM (Finite State Machine)
--------------------------
FSM ini mengatur alur parsing per baris.

State flow utama (simplified):

READ_LINE
  ↓
EXTRACT_CODE
  ↓
RESOLVE_SCHEMA
  ↓
PARSE_RECORD
  ↓
EMIT_RECORD
  ↺ kembali ke READ_LINE
*/

type State string

const (
	StateReadLine      State = "READ_LINE"
	StateExtractCode   State = "EXTRACT_CODE"
	StateResolveSchema State = "RESOLVE_SCHEMA"
	StateParseRecord   State = "PARSE_RECORD"
	StateEmitRecord    State = "EMIT_RECORD"
	StateError         State = "ERROR"
)

/*
FSMContext
----------
Menyimpan state & data sementara selama parsing satu baris
*/

type FSMContext struct {
	State State

	// raw line
	Line string

	// hasil split
	Fields []string

	// record code (000 / 001 / dll)
	RecordCode string

	// error jika ada
	Err error
}

// NewFSMContext inisialisasi FSM context
func NewFSMContext() *FSMContext {
	return &FSMContext{
		State: StateReadLine,
	}
}

// Reset FSM ke state awal untuk baris berikutnya
func (c *FSMContext) Reset() {
	c.State = StateReadLine
	c.Line = ""
	c.Fields = nil
	c.RecordCode = ""
	c.Err = nil
}
