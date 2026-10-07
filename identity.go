package linkedin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// CanonicalMemberURN normalizes mini-profile and dash-profile identities with the
// same immutable provider ID. Numeric member/person URNs are not assumed equivalent.
func CanonicalMemberURN(raw string) (string, error) {
	for _, prefix := range []string{"urn:li:fsd_profile:", "urn:li:fs_miniProfile:"} {
		if id, ok := strings.CutPrefix(raw, prefix); ok && validOpaqueID(id) {
			return "urn:li:fsd_profile:" + id, nil
		}
	}
	return "", ErrIdentityUnavailable
}

func validOpaqueID(id string) bool {
	if id == "" || len(id) > 256 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// GetMe proves the current authenticated member using /me, with a safe legacy
// read fallback. A missing or ambiguous authenticated reference fails closed.
func (c *Client) GetMe(ctx context.Context) (*Profile, error) {
	c.verifiedMember.Store("")
	for _, path := range []string{apiBase + "/me", apiBase + "/identity/profiles/me"} {
		body, err := c.makeRequest(ctx, path)
		if err != nil {
			if err == ErrNotFound || strings.Contains(err.Error(), "HTTP 410") {
				continue
			}
			return nil, err
		}
		profile, err := parseAuthenticatedProfile(body)
		if err == nil {
			c.verifiedMember.Store(profile.URN)
		}
		return profile, err
	}
	return nil, ErrIdentityUnavailable
}

func parseAuthenticatedProfile(body []byte) (*Profile, error) {
	type mini struct {
		URN        string `json:"entityUrn"`
		PublicID   string `json:"publicIdentifier"`
		FirstName  string `json:"firstName"`
		LastName   string `json:"lastName"`
		Headline   string `json:"headline"`
		Occupation string `json:"occupation"`
	}
	var response struct {
		mini
		Mini *mini  `json:"miniProfile"`
		Ref  string `json:"*miniProfile"`
		Data struct {
			mini
			Ref  string `json:"*miniProfile"`
			Mini *mini  `json:"miniProfile"`
		} `json:"data"`
		Included []mini `json:"included"`
	}
	if json.Unmarshal(body, &response) != nil {
		return nil, ErrIdentityUnavailable
	}
	var envelope map[string]any
	if json.Unmarshal(body, &envelope) != nil || hasErrorEnvelope(envelope) {
		return nil, ErrIdentityUnavailable
	}
	data, _ := envelope["data"].(map[string]any)
	if hasErrorEnvelope(data) {
		return nil, ErrIdentityUnavailable
	}
	for _, wrapper := range []map[string]any{envelope, data} {
		if selected, ok := wrapper["miniProfile"].(map[string]any); ok && hasErrorEnvelope(selected) {
			return nil, ErrIdentityUnavailable
		}
	}
	me := response.mini
	ref := response.Ref
	if response.Data.Ref != "" {
		if ref != "" && ref != response.Data.Ref {
			return nil, ErrIdentityUnavailable
		}
		ref = response.Data.Ref
	}
	if ref != "" {
		found := 0
		for _, item := range response.Included {
			if item.URN == ref {
				me = item
				found++
			}
		}
		if found != 1 {
			return nil, ErrIdentityUnavailable
		}
		entries, _ := envelope["included"].([]any)
		for _, item := range entries {
			selected, _ := item.(map[string]any)
			if firstString(selected, "entityUrn") == ref && hasErrorEnvelope(selected) {
				return nil, ErrIdentityUnavailable
			}
		}
	} else if response.Mini != nil {
		me = *response.Mini
	} else if response.Data.Mini != nil {
		me = *response.Data.Mini
	} else if response.Data.URN != "" {
		me = response.Data.mini
	}
	urn, err := CanonicalMemberURN(me.URN)
	if err != nil {
		return nil, err
	}
	// Every supplied self representation must agree. A valid reference must not
	// hide a conflicting direct profile in the same authenticated envelope.
	for _, candidate := range []*mini{&response.mini, &response.Data.mini, response.Mini, response.Data.Mini} {
		if candidate == nil || (candidate.URN == "" && candidate != response.Mini && candidate != response.Data.Mini) {
			continue
		}
		observed, err := CanonicalMemberURN(candidate.URN)
		if err != nil || observed != urn {
			return nil, ErrIdentityUnavailable
		}
	}
	if me.Headline == "" {
		me.Headline = me.Occupation
	}
	p := &Profile{URN: urn, PublicID: me.PublicID, FirstName: me.FirstName, LastName: me.LastName, Headline: me.Headline}
	if me.PublicID != "" {
		p.ProfileURL = "https://www.linkedin.com/in/" + url.PathEscape(me.PublicID)
	}
	return p, nil
}

func profileSlug(identifier string) (string, error) {
	slug := identifier
	if strings.Contains(identifier, "://") {
		u, err := url.Parse(identifier)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" ||
			(u.Hostname() != "linkedin.com" && u.Hostname() != "www.linkedin.com") || u.RawQuery != "" || u.Fragment != "" {
			return "", ErrInvalidParams
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) != 2 || parts[0] != "in" {
			return "", ErrInvalidParams
		}
		slug = parts[1]
	}
	if slug == "" || len(slug) > 200 || strings.ContainsAny(slug, "/?#:()\\ \t\r\n") {
		return "", ErrInvalidParams
	}
	return slug, nil
}

// ResolveMember requires an exact public identifier and separately reads sender
// relationship metadata. Missing metadata returns degree zero, not first degree.
func (c *Client) ResolveMember(ctx context.Context, identifier string) (*Member, error) {
	slug, err := profileSlug(identifier)
	if err != nil {
		return nil, fmt.Errorf("%w: expected a LinkedIn profile URL or vanity name", ErrInvalidParams)
	}
	profileBody, err := c.makeRequest(ctx, c.buildProfileURL(slug))
	if err != nil {
		return nil, err
	}
	var profileEnvelope map[string]any
	if json.Unmarshal(profileBody, &profileEnvelope) != nil || hasErrorEnvelope(profileEnvelope) {
		return nil, ErrParseFailed
	}
	profileData, _ := profileEnvelope["data"].(map[string]any)
	if hasErrorEnvelope(profileData) {
		return nil, ErrParseFailed
	}
	var response profileAPIResponse
	if json.Unmarshal(profileBody, &response) != nil {
		return nil, ErrParseFailed
	}
	matching := 0
	for _, item := range response.Included {
		if item.Type == typeProfile && strings.EqualFold(item.PublicIdentifier, slug) {
			matching++
		}
	}
	if matching == 0 {
		return nil, ErrNotFound
	}
	if matching != 1 {
		return nil, ErrIdentityUnavailable
	}
	entries, _ := profileEnvelope["included"].([]any)
	for _, item := range entries {
		selected, _ := item.(map[string]any)
		if strings.EqualFold(firstString(selected, "publicIdentifier"), slug) && hasErrorEnvelope(selected) {
			return nil, ErrParseFailed
		}
	}
	profile, err := parseProfileResponse(&response, slug)
	if err != nil {
		return nil, err
	}
	urn, err := CanonicalMemberURN(profile.URN)
	if err != nil {
		return nil, err
	}
	member := &Member{URN: urn, PublicID: profile.PublicID, ProfileURL: profile.ProfileURL, InvitationState: "unknown"}
	relationshipID := "urn:li:fsd_memberRelationship:" + strings.TrimPrefix(urn, "urn:li:fsd_profile:")
	body, err := c.makeRequest(ctx, apiBase+"/voyagerRelationshipsDashMemberRelationships/"+url.PathEscape(relationshipID))
	if err == nil {
		if err := parseRelationship(body, relationshipID, member, c.VerifiedMemberURN()); err != nil {
			return nil, err
		}
		return member, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	body, err = c.makeRequest(ctx, apiBase+"/identity/profiles/"+url.PathEscape(slug)+"/networkinfo")
	if err != nil {
		return nil, err
	}
	var envelope map[string]any
	if json.Unmarshal(body, &envelope) != nil || hasErrorEnvelope(envelope) {
		return nil, ErrParseFailed
	}
	data, _ := envelope["data"].(map[string]any)
	if hasErrorEnvelope(data) {
		return nil, ErrParseFailed
	}
	var networkResponse struct {
		Distance struct {
			Value string `json:"value"`
		} `json:"distance"`
		Data *struct {
			Distance struct {
				Value string `json:"value"`
			} `json:"distance"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &networkResponse) != nil {
		return nil, ErrParseFailed
	}
	distance := networkResponse.Distance.Value
	if networkResponse.Data != nil {
		if distance != "" && networkResponse.Data.Distance.Value != distance {
			return nil, ErrParseFailed
		}
		distance = networkResponse.Data.Distance.Value
	}
	switch distance {
	case "DISTANCE_1":
		member.ConnectionDegree = 1
		member.InvitationState = "connected"
	case "DISTANCE_2":
		member.ConnectionDegree = 2
	case "DISTANCE_3":
		member.ConnectionDegree = 3
	}
	// Legacy networkinfo supplies no proven invitation-note eligibility.
	return member, nil
}

func parseRelationship(body []byte, wanted string, member *Member, viewer string) error {
	var root map[string]any
	if json.Unmarshal(body, &root) != nil {
		return ErrParseFailed
	}
	if hasErrorEnvelope(root) {
		return ErrParseFailed
	}
	data, _ := root["data"].(map[string]any)
	if data == nil || hasErrorEnvelope(data) {
		return nil
	}
	if observed := firstString(data, "entityUrn"); observed != "" && observed != wanted {
		return ErrIdentityUnavailable
	}
	union, _ := data["memberRelationshipUnion"].(map[string]any)
	if union == nil {
		return nil
	}
	if hasErrorEnvelope(union) {
		return ErrParseFailed
	}
	connection := firstString(union, "*connection")
	noConnection, hasNoConnection := union["noConnection"].(map[string]any)
	if connection != "" {
		if hasNoConnection || !validConnectionURN(connection) {
			return ErrParseFailed
		}
		if suffix, _ := strings.CutPrefix(connection, "urn:li:fsd_connection:"); strings.HasPrefix(suffix, "(") {
			parts := strings.Split(suffix[1:len(suffix)-1], ",")
			recipientID := strings.TrimPrefix(member.URN, "urn:li:fsd_profile:")
			if parts[0] != recipientID && parts[1] != recipientID {
				return ErrRecipientIdentityMismatch
			}
			if viewer == "" {
				return nil
			}
			viewerID := strings.TrimPrefix(viewer, "urn:li:fsd_profile:")
			if viewerID == recipientID || !((parts[0] == viewerID && parts[1] == recipientID) || (parts[1] == viewerID && parts[0] == recipientID)) {
				return ErrSenderIdentityMismatch
			}
		}
		if !uniqueIncludedReference(root, connection) {
			return nil
		}
		entries, _ := root["included"].([]any)
		associated := false
		for _, entry := range entries {
			obj, _ := entry.(map[string]any)
			if firstString(obj, "entityUrn") != connection {
				continue
			}
			if hasErrorEnvelope(obj) {
				return ErrParseFailed
			}
			observed, err := connectedMemberURN(obj)
			if err != nil {
				return nil
			}
			if observed != member.URN {
				return ErrRecipientIdentityMismatch
			}
			associated = true
		}
		if !associated {
			return nil
		}
		member.ConnectionDegree = 1
		member.InvitationState = "connected"
		return nil
	}
	if !hasNoConnection {
		return nil
	}
	if hasErrorEnvelope(noConnection) {
		return ErrParseFailed
	}
	switch firstString(noConnection, "memberDistance") {
	case "DISTANCE_1":
		return ErrParseFailed
	case "DISTANCE_2":
		member.ConnectionDegree = 2
	case "DISTANCE_3":
		member.ConnectionDegree = 3
	}
	invitation, _ := noConnection["invitationUnion"].(map[string]any)
	if hasErrorEnvelope(invitation) {
		return ErrParseFailed
	}
	ref := firstString(invitation, "*invitation")
	noInvitationValue, noInvitation := invitation["noInvitation"]
	if noInvitation {
		proof, ok := noInvitationValue.(map[string]any)
		noInvitation = ok && !hasErrorEnvelope(proof)
	}
	if ref != "" {
		if noInvitation || !receiptURN(ref, "urn:li:fsd_invitation:") {
			return ErrParseFailed
		}
		entries, _ := root["included"].([]any)
		if !uniqueIncludedReference(root, ref) {
			return nil
		}
		for _, entry := range entries {
			obj, _ := entry.(map[string]any)
			if firstString(obj, "entityUrn") == ref && firstString(obj, "invitationState") == "PENDING" {
				member.InvitationState = "pending"
			}
		}
		return nil
	}
	// The exact relationship resource can prove no connection/invitation without
	// supplying memberDistance. Distance remains independent DM evidence; an
	// explicit DISTANCE_1/noConnection contradiction was rejected above.
	if noInvitation && firstString(data, "entityUrn") == wanted {
		member.InvitationState = "available"
	}
	return nil
}

func validConnectionURN(raw string) bool {
	if suffix, ok := strings.CutPrefix(raw, "urn:li:fsd_connection:"); ok {
		if validOpaqueID(suffix) {
			return true
		}
		if strings.HasPrefix(suffix, "(") && strings.HasSuffix(suffix, ")") {
			parts := strings.Split(suffix[1:len(suffix)-1], ",")
			return len(parts) == 2 && validOpaqueID(parts[0]) && validOpaqueID(parts[1])
		}
	}
	return false
}

func connectedMemberURN(obj map[string]any) (string, error) {
	found := ""
	for _, key := range []string{"connectedMember", "*connectedMember", "*connectedMemberResolutionResult"} {
		value, exists := obj[key]
		if !exists {
			continue
		}
		raw, _ := value.(string)
		if nested, ok := value.(map[string]any); ok && !hasErrorEnvelope(nested) {
			raw = firstString(nested, "entityUrn", "profileUrn")
		}
		member, err := CanonicalMemberURN(raw)
		if err != nil || (found != "" && found != member) {
			return "", ErrIdentityUnavailable
		}
		found = member
	}
	if found == "" {
		return "", ErrIdentityUnavailable
	}
	return found, nil
}

func uniqueIncludedReference(root map[string]any, ref string) bool {
	entries, _ := root["included"].([]any)
	count := 0
	for _, entry := range entries {
		obj, _ := entry.(map[string]any)
		if firstString(obj, "entityUrn") == ref {
			count++
		}
	}
	return count == 1
}
