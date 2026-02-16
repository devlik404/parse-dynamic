package valueobject

import "strings"

/*
RecordCode
----------
Value object untuk identitas record type
*/

type RecordCode string

func NewRecordCode(raw string) RecordCode {
	return RecordCode(strings.TrimSpace(raw))
}

func (r RecordCode) String() string {
	return string(r)
}

func (r RecordCode) IsEmpty() bool {
	return r.String() == ""
}
