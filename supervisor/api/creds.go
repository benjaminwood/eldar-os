package api

import (
	"encoding/json"
	"sort"
	"strings"
)

// Creds is a credential scope: either "any" (the JSON string "*") or an
// explicit set of names (a JSON array, possibly empty).
type Creds struct {
	Any   bool
	Names []string
}

// AnyCreds matches every request, including ones that inject a whole profile.
var AnyCreds = Creds{Any: true}

func (c Creds) MarshalJSON() ([]byte, error) {
	if c.Any {
		return json.Marshal("*")
	}
	names := c.Names
	if names == nil {
		names = []string{}
	}
	return json.Marshal(names)
}

func (c *Creds) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*c = Creds{Any: s == "*"}
		return nil
	}
	var names []string
	if err := json.Unmarshal(b, &names); err != nil {
		return err
	}
	sort.Strings(names)
	*c = Creds{Names: names}
	return nil
}

func (c Creds) String() string {
	if c.Any {
		return "any credentials"
	}
	if len(c.Names) == 0 {
		return "no credentials"
	}
	return strings.Join(c.Names, ", ")
}
