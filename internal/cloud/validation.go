package cloud

// ValidationError describes a rejected response or account input using trusted field/rule labels.
// It retains the existing error class through Unwrap and never includes values.
type ValidationError struct {
	Field, Rule string
	cause       error
}

func (e *ValidationError) Error() string { return "cloud: validation " + e.Field + ": " + e.Rule }
func (e *ValidationError) Unwrap() error { return e.cause }

func validation(field, rule string, cause error) error {
	return &ValidationError{Field: field, Rule: rule, cause: cause}
}
func invalid(field, rule string) error { return validation(field, rule, ErrInvalid) }
