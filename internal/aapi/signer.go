package aapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/yashiels/amazon-za-cli/internal/fault"
)

type Key struct {
	ID     string
	Secret []byte
}
type Header struct{ Name, Value string }

func javaEncode(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(url.QueryEscape(s), "%2A", "*"), "~", "%7E")
}

func Sign(method, path, rawQuery string, headers []Header, at time.Time, key Key) (string, error) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", fault.New("unsafe_request", "invalid query")
	}
	pairs := make([]string, 0)
	for k, values := range query {
		for _, v := range values {
			pairs = append(pairs, javaEncode(k)+"="+javaEncode(v))
		}
	}
	sort.Strings(pairs)
	values := make(map[string]string)
	names := make([]string, 0, len(headers))
	for _, h := range headers {
		name := strings.ToLower(h.Name)
		if _, exists := values[name]; exists {
			return "", fault.New("unsafe_request", "duplicate header")
		}
		values[name] = h.Value
		if name != "x-amzn-trace-id" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	timestamp := at.UTC().Format("2006-01-02T15:04:05-0700")
	signed := strings.Join(names, ";")
	var canonical strings.Builder
	canonical.WriteString(strings.Join([]string{method, timestamp, key.ID, "HmacSHA256", path, strings.Join(pairs, "&"), ""}, "\n"))
	for _, name := range names {
		canonical.WriteString(name + ":" + values[name] + "\n")
	}
	canonical.WriteString(signed)
	mac := hmac.New(sha256.New, key.Secret)
	_, _ = mac.Write([]byte(canonical.String()))
	return "HmacSHA256 Time=" + timestamp + " SignedHeaders=" + signed + " Signature=" + hex.EncodeToString(mac.Sum(nil)) + "/" + key.ID, nil
}
