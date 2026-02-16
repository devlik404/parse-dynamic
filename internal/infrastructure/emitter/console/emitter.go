package console

import (
	"fmt"

	"parser-engine/internal/domain/parser"
)

type Emitter struct{}

func New() *Emitter {
	return &Emitter{}
}

func (e *Emitter) Emit(r parser.Record) error {
	fmt.Printf(
		"[RECORD] rule=%s section=%s line=%d data=%v\n",
		r.RuleID,
		r.Section,
		r.LineNumber,
		r.Fields,
	)
	return nil
}
