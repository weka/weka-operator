package util

import (
	"reflect"
	"runtime"
	"strings"
)

func GetFunctionName(i interface{}) string {
	baseName := runtime.FuncForPC(reflect.ValueOf(i).Pointer()).Name()
	//return last part
	parts := strings.Split(baseName, ".")
	return parts[len(parts)-1]
}
