package resources

// Reconcilable: a resource that can be reconciled
type Reconcilable interface {
	// Get: get the resource
	Get() (interface{}, error)
}
