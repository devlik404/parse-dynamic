package port

import "parser-engine/internal/domain/parser"

/*
RecordEmitter
-------------
Output port untuk hasil parsing.
*/

type RecordEmitter interface {
	Emit(record parser.Record) error
}
