//go:generate go run go.uber.org/mock/mockgen@latest -destination=mocks/mock_reconciler.go -package=mocks github.com/weka/weka-operator/internal/app/manager/controllers/lifecycle Reconciler
package cluster
