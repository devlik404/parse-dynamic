package valueobject

import "fmt"

/*
ParseError
----------
Domain error untuk parsing
*/

type ParseError struct {
	LineNumber int
	Reason     string
	RawLine    string
}

func (e ParseError) Error() string {
	return fmt.Sprintf(
		"parse error at line %d: %s | raw: %s",
		e.LineNumber,
		e.Reason,
		e.RawLine,
	)
}
