package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

func decodeJSON(data []byte, cfg *Config) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(cfg); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON documents are not allowed")
		}
		return err
	}
	return nil
}
