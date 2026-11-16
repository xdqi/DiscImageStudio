package dvd

// .NET-flavored error types. Error() returns exactly what the C#
// Exception.Message property would contain so RunCommand's catch can print
// "error: {message}" identically.

// argumentError mirrors ArgumentException (optionally with ParamName).
type argumentError struct {
	msg   string
	param string
}

func (e *argumentError) Error() string {
	if e.param == "" {
		return e.msg
	}
	return e.msg + " (Parameter '" + e.param + "')"
}

func newArgumentError(msg string) error { return &argumentError{msg: msg} }

func newArgumentErrorParam(msg, param string) error { return &argumentError{msg: msg, param: param} }

// argumentOutOfRangeError mirrors ArgumentOutOfRangeException(paramName, message)
// and ArgumentOutOfRangeException(paramName).
type argumentOutOfRangeError struct {
	param string
	msg   string // empty => the default "Specified argument was out of the range of valid values."
}

func (e *argumentOutOfRangeError) Error() string {
	msg := e.msg
	if msg == "" {
		msg = "Specified argument was out of the range of valid values."
	}
	if e.param == "" {
		return msg
	}
	return msg + " (Parameter '" + e.param + "')"
}

// formatError mirrors FormatException.
type formatError struct{ msg string }

func (e *formatError) Error() string { return e.msg }

// overflowError mirrors OverflowException.
type overflowError struct{ msg string }

func (e *overflowError) Error() string { return e.msg }

func newOverflowError(msg string) error { return &overflowError{msg: msg} }

// invalidOperationError mirrors InvalidOperationException.
type invalidOperationError struct{ msg string }

func (e *invalidOperationError) Error() string { return e.msg }

func newInvalidOperationError(msg string) error { return &invalidOperationError{msg: msg} }

// checkedAddU32 mirrors C# checked(uint + uint).
func checkedAddU32(a, b uint32) (uint32, error) {
	result := a + b
	if result < a {
		return 0, newOverflowError("Arithmetic operation resulted in an overflow.")
	}
	return result, nil
}

// checkedSubU32 mirrors C# checked(uint - uint).
func checkedSubU32(a, b uint32) (uint32, error) {
	if a < b {
		return 0, newOverflowError("Arithmetic operation resulted in an overflow.")
	}
	return a - b, nil
}
