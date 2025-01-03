package controllers

type NoWekaFsDriverFound struct{}

func (e *NoWekaFsDriverFound) Error() string {
	return "No wekafs driver found"
}
