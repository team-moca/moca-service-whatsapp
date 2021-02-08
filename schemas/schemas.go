package schemas

import (
	"fmt"
	"io/ioutil"
)

var schemas = make(map[string][]byte)

func Get(schema string) ([]byte, error) {
	s, found := schemas[schema]
	if !found {
		fmt.Printf("Schema not in cache. Try to load it from disk now...")

		s2, err := ioutil.ReadFile("schemas/" + schema + ".schema.json") // WARNING! This is not user facing, but schema can be used for a directory traversal attack.

		if err != nil {
			return nil, err
		}

		schemas[schema] = s2
		return s2, nil
	}

	return s, nil
}
