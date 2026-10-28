package aliyuncollector

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// object rejects duplicate keys before decoding. Choosing either duplicate value
// would let a newer provider field or conflicting identity disappear silently.
func object(raw []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	tokens := 0
	var visit func(int) error
	visit = func(depth int) error {
		if depth > 32 {
			return Failure("PROVIDER_RESPONSE_INVALID")
		}
		tok, err := d.Token()
		if err != nil {
			return Failure("PROVIDER_RESPONSE_INVALID")
		}
		tokens++
		if tokens > 300000 {
			return Failure("PROVIDER_RESPONSE_INVALID")
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return Failure("PROVIDER_RESPONSE_INVALID")
				}
				key, ok := k.(string)
				if !ok || seen[key] {
					return Failure("PROVIDER_RESPONSE_INVALID")
				}
				seen[key] = true
				if e = visit(depth + 1); e != nil {
					return e
				}
			}
			last, e := d.Token()
			if e != nil || last != json.Delim('}') {
				return Failure("PROVIDER_RESPONSE_INVALID")
			}
		case '[':
			for d.More() {
				if e := visit(depth + 1); e != nil {
					return e
				}
			}
			last, e := d.Token()
			if e != nil || last != json.Delim(']') {
				return Failure("PROVIDER_RESPONSE_INVALID")
			}
		default:
			return Failure("PROVIDER_RESPONSE_INVALID")
		}
		return nil
	}
	if err := visit(0); err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, Failure("PROVIDER_RESPONSE_INVALID")
	}
	var o map[string]json.RawMessage
	if json.Unmarshal(raw, &o) != nil || o == nil {
		return nil, Failure("PROVIDER_RESPONSE_INVALID")
	}
	return o, nil
}
func list(o map[string]json.RawMessage, key string) ([]map[string]json.RawMessage, bool, error) {
	raw, ok := o[key]
	if !ok || bytes.Equal(raw, []byte("null")) {
		return nil, false, nil
	}
	var rows []json.RawMessage
	if json.Unmarshal(raw, &rows) != nil || rows == nil {
		return nil, false, Failure("PROVIDER_RESPONSE_INVALID")
	}
	out := make([]map[string]json.RawMessage, 0, len(rows))
	for _, raw := range rows {
		row, err := object(raw)
		if err != nil {
			return nil, false, err
		}
		out = append(out, row)
	}
	return out, true, nil
}
func totalCount(o map[string]json.RawMessage, stringNumber bool) (int, bool) {
	raw, ok := o["TotalCount"]
	if !ok {
		return 0, false
	}
	s := string(raw)
	if stringNumber {
		var v string
		if json.Unmarshal(raw, &v) != nil {
			return 0, false
		}
		s = v
	}
	if !decimal(s) || len(s) > 9 {
		return 0, false
	}
	n, e := strconv.Atoi(s)
	return n, e == nil
}

var volatileFields = map[string]bool{"RequestId": true, "HitTimes": true, "HitLastTime": true, "Last7DayOutTrafficBytes": true, "SgStatusTime": true}

func canonicalValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		o := map[string]any{}
		for k, v := range x {
			if !volatileFields[k] {
				o[k] = canonicalValue(v)
			}
		}
		return o
	case []any:
		o := make([]any, len(x))
		for i, v := range x {
			o[i] = canonicalValue(v)
		}
		return o
	}
	return v
}
func semantic(raw json.RawMessage) string {
	var v any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	_ = d.Decode(&v)
	b, _ := json.Marshal(canonicalValue(v))
	return string(b)
}
func rowFingerprint(rows []map[string]json.RawMessage, extras []string) string {
	values := append([]string(nil), extras...)
	for _, row := range rows {
		b, _ := json.Marshal(row)
		values = append(values, semantic(b))
	}
	sort.Strings(values)
	return hash([]byte(strings.Join(values, "\x00")))
}

func (r *run) pages(round int, op string, filters map[string]string, array, pageKey string, stringTotal bool, idKey string, limit int) (rows []map[string]json.RawMessage, complete bool, err error) {
	obs := Observation{Round: round, Operation: op, Filters: cloneQuery(filters), StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	extras := []string{}
	defer func() {
		obs.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		obs.EnumerationComplete = complete
		obs.SemanticSHA256 = rowFingerprint(rows, extras)
		r.observations = append(r.observations, obs)
	}()
	seen := map[string]string{}
	expected := -1
	pageSize := r.collector.cfg.PageSize
	for page := 1; page <= limit+1; page++ {
		query := cloneQuery(filters)
		query[pageKey] = strconv.Itoa(page)
		query["PageSize"] = strconv.Itoa(pageSize)
		raw, e := r.read(round, op, query)
		if e != nil {
			return rows, false, e
		}
		obs.Pages++
		body, e := object(raw)
		if e != nil {
			return rows, false, e
		}
		for key, value := range body {
			if key != array && key != "TotalCount" && key != "PageNo" && key != "PageSize" && key != "RequestId" {
				extras = append(extras, key+":"+semantic(value))
				return rows, false, nil
			}
		}
		current, known := totalCount(body, stringTotal)
		batch, present, e := list(body, array)
		if e != nil {
			return rows, false, e
		}
		if !known || !present {
			return rows, false, nil
		}
		if expected < 0 {
			expected = current
		} else if expected != current {
			return rows, false, nil
		}
		if rawPage, present := body["PageNo"]; present {
			v := strings.Trim(string(rawPage), "\"")
			if v != strconv.Itoa(page) {
				return rows, false, nil
			}
		}
		if rawSize, present := body["PageSize"]; present {
			v := strings.Trim(string(rawSize), "\"")
			if v != strconv.Itoa(pageSize) {
				return rows, false, nil
			}
		}
		if len(batch) > pageSize || len(rows)+len(batch) > current {
			return rows, false, nil
		}
		for _, row := range batch {
			b, _ := json.Marshal(row)
			identity := hash(b)
			if idKey != "" {
				identity = text(row, idKey)
				if !alias(identity) {
					return rows, false, Failure("PROVIDER_RESPONSE_INVALID")
				}
			}
			encoded := semantic(b)
			if old, exists := seen[identity]; exists {
				if old != encoded {
					return rows, false, Failure("PROVIDER_IDENTITY_CONFLICT")
				}
				return rows, false, nil
			}
			seen[identity] = encoded
			if len(rows) >= limit {
				return rows, false, nil
			}
			rows = append(rows, row)
		}
		if len(rows) == current {
			return rows, true, nil
		}
		if current > limit || len(batch) != pageSize {
			return rows, false, nil
		}
	}
	return rows, false, nil
}
