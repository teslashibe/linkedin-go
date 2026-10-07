package linkedin

import (
	"encoding/json"
	"reflect"
	"strings"
)

// createdValue follows only an explicit creation result or its referenced
// included entity. Unrelated included records cannot become delivery receipts.
func createdValue(body []byte) (map[string]any, error) {
	var root map[string]any
	if json.Unmarshal(body, &root) != nil {
		return nil, unknownReceipt()
	}
	if hasErrorEnvelope(root) {
		return nil, unknownReceipt()
	}
	data, _ := root["data"].(map[string]any)
	if hasErrorEnvelope(data) {
		return nil, unknownReceipt()
	}
	ref := firstString(data, "*value")
	if other := firstString(root, "*value"); other != "" {
		if ref != "" && ref != other {
			return nil, unknownReceipt()
		}
		ref = other
	}
	var selected map[string]any
	if ref != "" {
		items, _ := root["included"].([]any)
		for _, item := range items {
			obj, _ := item.(map[string]any)
			if firstString(obj, "entityUrn") == ref {
				if selected != nil {
					return nil, unknownReceipt()
				}
				selected = obj
			}
		}
		if selected == nil || hasErrorEnvelope(selected) {
			return nil, unknownReceipt()
		}
	}
	for _, envelope := range []map[string]any{data, root} {
		if value, ok := envelope["value"].(map[string]any); ok {
			if hasErrorEnvelope(value) {
				return nil, unknownReceipt()
			}
			if comment, ok := value["createdComment"].(map[string]any); ok {
				value = comment
			}
			if selected != nil && !reflect.DeepEqual(selected, value) {
				return nil, unknownReceipt()
			}
			selected = value
		}
		if firstString(envelope, "entityUrn") != "" {
			if selected != nil && !reflect.DeepEqual(selected, envelope) {
				return nil, unknownReceipt()
			}
			selected = envelope
		}
	}
	if selected == nil || hasErrorEnvelope(selected) {
		return nil, unknownReceipt()
	}
	return selected, nil
}

func hasErrorEnvelope(obj map[string]any) bool {
	if len(obj) == 0 {
		return false
	}
	for _, key := range []string{"errors", "error"} {
		if value, exists := obj[key]; exists && value != nil {
			return true
		}
	}
	if status, ok := obj["status"].(float64); ok && status >= 400 {
		return true
	}
	if code, exists := obj["code"]; exists && code != nil && code != "" && code != float64(0) {
		return true
	}
	if code, exists := obj["serviceErrorCode"]; exists && code != nil && code != "" && code != float64(0) {
		return true
	}
	return false
}

func valueText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	if obj, ok := value.(map[string]any); ok {
		return firstText(obj, "text")
	}
	return ""
}

func receiptURN(raw string, prefixes ...string) bool {
	if len(raw) > 512 || strings.ContainsAny(raw, " \t\r\n?#%\\") {
		return false
	}
	for _, prefix := range prefixes {
		if suffix, ok := strings.CutPrefix(raw, prefix); ok {
			if validReceiptID(suffix) {
				return true
			}
			if prefix != "urn:li:msg_message:" && prefix != "urn:li:msg_conversation:" {
				return false
			}
			if !strings.HasPrefix(suffix, "(") || !strings.HasSuffix(suffix, ")") {
				return false
			}
			parts := strings.Split(suffix[1:len(suffix)-1], ",")
			if len(parts) != 2 || !validReceiptID(parts[1]) {
				return false
			}
			_, err := CanonicalMemberURN(parts[0])
			return err == nil
		}
	}
	return false
}

func validReceiptID(id string) bool {
	if id == "" || len(id) > 256 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_=+/", r)) {
			return false
		}
	}
	return true
}

func receiptMember(raw string) string {
	index := strings.Index(raw, ":(")
	if index < 0 {
		return ""
	}
	parts := strings.Split(raw[index+2:len(raw)-1], ",")
	member, _ := CanonicalMemberURN(parts[0])
	return member
}

func selectMessagingReceipt(value map[string]any, keys []string, viewer string, prefixes ...string) (string, bool) {
	selected, selectedID := "", ""
	for _, key := range keys {
		observed, exists := value[key]
		if !exists {
			continue
		}
		urn, ok := observed.(string)
		if !ok || !receiptURN(urn, prefixes...) {
			return "", false
		}
		if member := receiptMember(urn); member != "" && member != viewer {
			return "", false
		}
		id := urn[strings.LastIndex(urn, ":")+1:]
		if index := strings.Index(urn, ":("); index >= 0 {
			id = strings.Split(urn[index+2:len(urn)-1], ",")[1]
		}
		if selectedID != "" && selectedID != id {
			return "", false
		}
		if selected == "" {
			selected, selectedID = urn, id
		}
	}
	return selected, selected != ""
}
