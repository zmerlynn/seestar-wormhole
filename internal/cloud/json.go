package cloud

import (
	"errors"
	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
)

// response holds only the common envelope fields we consume.
// Data is decoded separately so refusal codes take precedence over payload errors.
type response struct {
	Code *int           `json:"code"`
	Data jsontext.Value `json:"data"`
}

func readResponse(b []byte) (response, error) {
	if len(b) > MaxBody {
		return response{}, invalid("body", "body_limit")
	}
	// Decode raw fields first so a semantic code error cannot hide data.conId
	// needed for cleanup. V2 validates duplicates even in raw/unknown members.
	var raw *struct {
		Code jsontext.Value `json:"code"`
		Data jsontext.Value `json:"data"`
	}
	if err := decodeJSON(b, &raw); err != nil {
		return response{}, err
	}
	if raw == nil {
		return response{}, invalid("body", "object")
	}
	r := response{Data: raw.Data}
	if len(raw.Code) != 0 && json.Unmarshal(raw.Code, &r.Code) != nil {
		return r, invalid("code", "integer")
	}
	return r, nil
}

// Never expose JSON decoder errors: they can contain response values or keys.
func decodeJSON(b []byte, dst any) error {
	if err := json.Unmarshal(b, dst); err != nil {
		var typeErr *json.SemanticError
		if errors.As(err, &typeErr) {
			switch string(typeErr.JSONPointer) {
			case "/code":
				return invalid("code", "integer")
			case "/accessToken":
				return invalid("data.accessToken", "string")
			case "/password":
				return invalid("data.password", "nonblank_string_without_nul")
			case "/items":
				return invalid("data.items", "array_or_zero_total")
			}
			return invalid("body", "field_type")
		}
		return invalid("body", "json_syntax")
	}
	return nil
}
