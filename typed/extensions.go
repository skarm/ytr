package typed

import (
	"encoding/json"
	"fmt"

	"github.com/skarm/ytr"
)

type extendedBody struct {
	Value any
	Extra ytr.Fields
}

func (b extendedBody) MarshalJSON() ([]byte, error) {
	data, err := json.Marshal(b.Value)
	if err != nil || len(b.Extra) == 0 {
		return data, err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}

	for key := range b.Extra {
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("ytr/typed: extension field %q duplicates a documented field", key)
		}
	}

	extra, err := json.Marshal(b.Extra)
	if err != nil {
		return nil, err
	}
	// Both parts are JSON objects produced by encoding/json. Splice their members
	// instead of converting the typed request into a map and encoding it again.
	res := append([]byte(nil), data[:len(data)-1]...)
	if len(fields) > 0 {
		res = append(res, ',')
	}

	return append(res, extra[1:]...), nil
}

func rawFields(fields map[string]json.RawMessage) ytr.Fields {
	if len(fields) == 0 {
		return nil
	}

	res := make(ytr.Fields, len(fields))
	for key, value := range fields {
		res[key] = value
	}

	return res
}

func withExtraFields(value any, extra ytr.Fields) any {
	if len(extra) == 0 {
		return value
	}

	return extendedBody{Value: value, Extra: extra}
}
