package util

import "github.com/sethvargo/go-password/password"

func GeneratePassword(length int) string {
	pass, err := password.Generate(length, 10, 0, false, true)
	if err != nil {
		panic(err)
	}
	return pass
}
