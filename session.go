package linkedin

import "strings"

// VerifiedMemberURN reports the last strict GetMe proof. A fresh GetMe clears
// it before requesting, and a different account remains a different identity.
// Hosts compare this with their approved sender before persisting rotations.
func (c *Client) VerifiedMemberURN() string {
	if c == nil {
		return ""
	}
	member, _ := c.verifiedMember.Load().(string)
	return member
}

// AuthSnapshot exports only this LinkedIn origin's live jar after an operation.
// Hosts own encrypted persistence and session revision fencing.
func (c *Client) AuthSnapshot() map[string]string {
	if c == nil || c.jar == nil {
		return nil
	}
	result := map[string]string{}
	for _, cookie := range c.jar.Cookies(linkedinBaseURL) {
		if cookie.Name != "" && cookie.Value != "" {
			result[cookie.Name] = strings.ReplaceAll(cookie.Value, `"`, "")
		}
	}
	return result
}

func (c *Client) sessionCSRF() string {
	if c.jar != nil {
		for _, cookie := range c.jar.Cookies(linkedinBaseURL) {
			if cookie.Name == "JSESSIONID" && cookie.Value != "" {
				return strings.ReplaceAll(cookie.Value, `"`, "")
			}
		}
	}
	return c.auth.CSRF
}
