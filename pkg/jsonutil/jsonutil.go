package jsonutil

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/vladislavprovich/gsp-secret-injector/pkg/stringutil"
)

func ConvertUnicodeToASCII(data []byte) []byte {
	// less than
	data = bytes.Replace(data, []byte("\\u003c"), []byte("<"), -1)
	// greater than
	data = bytes.Replace(data, []byte("\\u003e"), []byte(">"), -1)
	// ampersand
	data = bytes.Replace(data, []byte("\\u0026"), []byte("&"), -1)
	// backspace
	data = bytes.Replace(data, []byte("\\u0008"), []byte("\\b"), -1)
	// form feed
	data = bytes.Replace(data, []byte("\\u000c"), []byte("\\f"), -1)

	return data
}

func Flatten(jsonBytes []byte, path, formatter string) []string {
	result := gjson.GetBytes(jsonBytes, path)

	return _recursivelyFlatten("", result, formatter)
}

func _recursivelyFlatten(parent string, result gjson.Result, formatter string) []string {
	s := make([]string, 0)
	result.ForEach(func(key, value gjson.Result) bool {
		keyName := strings.ToUpper(key.String())
		if !stringutil.IsBlank(parent) {
			keyName = strings.Join([]string{parent, keyName}, "_")
		}
		if value.Type == gjson.JSON {
			s = append(s, _recursivelyFlatten(keyName, value, formatter)...)
		} else {
			s = append(s, fmt.Sprintf(formatter, keyName, value.String()))
		}
		return true
	})

	return s
}
