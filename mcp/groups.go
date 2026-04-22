package mcp

import (
	"context"

	linkedin "github.com/teslashibe/linkedin-go"
	"github.com/teslashibe/mcptool"
)

// SearchGroupsInput is the typed input for linkedin_search_groups.
type SearchGroupsInput struct {
	Query string `json:"query" jsonschema:"description=keywords to search groups for,required"`
	Start int    `json:"start,omitempty" jsonschema:"description=pagination offset,minimum=0,default=0"`
	Count int    `json:"count,omitempty" jsonschema:"description=results per page,minimum=1,maximum=49,default=10"`
}

func searchGroups(ctx context.Context, c *linkedin.Client, in SearchGroupsInput) (any, error) {
	res, err := c.SearchGroups(ctx, linkedin.GroupSearchParams{
		Keywords: in.Query,
		Start:    in.Start,
		Count:    in.Count,
	})
	if err != nil {
		return nil, err
	}
	limit := in.Count
	if limit <= 0 {
		limit = 10
	}
	return mcptool.PageOf(res, "", limit), nil
}

// GetGroupInput is the typed input for linkedin_get_group.
type GetGroupInput struct {
	GroupID string `json:"group_id" jsonschema:"description=LinkedIn group ID (numeric portion of the group URL),required"`
}

func getGroup(ctx context.Context, c *linkedin.Client, in GetGroupInput) (any, error) {
	return c.GetGroup(ctx, in.GroupID)
}

// GetGroupPostsInput is the typed input for linkedin_get_group_posts.
type GetGroupPostsInput struct {
	GroupID string `json:"group_id" jsonschema:"description=LinkedIn group ID,required"`
	SortBy  string `json:"sort_by,omitempty" jsonschema:"description=sort order; allowed: RECENT,TOP,default=RECENT"`
	Start   int    `json:"start,omitempty" jsonschema:"description=pagination offset,minimum=0,default=0"`
	Count   int    `json:"count,omitempty" jsonschema:"description=results per page,minimum=1,maximum=49,default=10"`
}

func getGroupPosts(ctx context.Context, c *linkedin.Client, in GetGroupPostsInput) (any, error) {
	res, err := c.GetGroupPosts(ctx, linkedin.GroupPostParams{
		GroupID: in.GroupID,
		SortBy:  in.SortBy,
		Start:   in.Start,
		Count:   in.Count,
	})
	if err != nil {
		return nil, err
	}
	limit := in.Count
	if limit <= 0 {
		limit = 10
	}
	return mcptool.PageOf(res, "", limit), nil
}

// GetGroupMembersInput is the typed input for linkedin_get_group_members.
type GetGroupMembersInput struct {
	GroupID string `json:"group_id" jsonschema:"description=LinkedIn group ID,required"`
	Role    string `json:"role,omitempty" jsonschema:"description=filter by role; allowed: OWNER,MANAGER,MEMBER, or empty for all"`
	Start   int    `json:"start,omitempty" jsonschema:"description=pagination offset,minimum=0,default=0"`
	Count   int    `json:"count,omitempty" jsonschema:"description=results per page,minimum=1,maximum=49,default=10"`
}

func getGroupMembers(ctx context.Context, c *linkedin.Client, in GetGroupMembersInput) (any, error) {
	res, err := c.GetGroupMembers(ctx, linkedin.GroupMemberParams{
		GroupID: in.GroupID,
		Role:    in.Role,
		Start:   in.Start,
		Count:   in.Count,
	})
	if err != nil {
		return nil, err
	}
	limit := in.Count
	if limit <= 0 {
		limit = 10
	}
	return mcptool.PageOf(res, "", limit), nil
}

// GetMyGroupsInput is the typed input for linkedin_get_my_groups.
type GetMyGroupsInput struct{}

func getMyGroups(ctx context.Context, c *linkedin.Client, _ GetMyGroupsInput) (any, error) {
	return c.GetMyGroups(ctx)
}

// JoinGroupInput is the typed input for linkedin_join_group.
type JoinGroupInput struct {
	GroupID string `json:"group_id" jsonschema:"description=LinkedIn group ID to join,required"`
}

func joinGroup(ctx context.Context, c *linkedin.Client, in JoinGroupInput) (any, error) {
	if err := c.JoinGroup(ctx, in.GroupID); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "group_id": in.GroupID}, nil
}

// LeaveGroupInput is the typed input for linkedin_leave_group.
type LeaveGroupInput struct {
	GroupID string `json:"group_id" jsonschema:"description=LinkedIn group ID to leave,required"`
}

func leaveGroup(ctx context.Context, c *linkedin.Client, in LeaveGroupInput) (any, error) {
	if err := c.LeaveGroup(ctx, in.GroupID); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "group_id": in.GroupID}, nil
}

// CreateGroupPostInput is the typed input for linkedin_create_group_post.
type CreateGroupPostInput struct {
	GroupID string `json:"group_id" jsonschema:"description=LinkedIn group ID to post in,required"`
	Text    string `json:"text" jsonschema:"description=plain-text post body,required"`
}

func createGroupPost(ctx context.Context, c *linkedin.Client, in CreateGroupPostInput) (any, error) {
	if err := c.CreateGroupPost(ctx, linkedin.CreateGroupPostParams{
		GroupID: in.GroupID,
		Text:    in.Text,
	}); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "group_id": in.GroupID}, nil
}

var groupTools = []mcptool.Tool{
	mcptool.Define[*linkedin.Client, SearchGroupsInput](
		"linkedin_search_groups",
		"Search LinkedIn groups by keyword",
		"SearchGroups",
		searchGroups,
	),
	mcptool.Define[*linkedin.Client, GetGroupInput](
		"linkedin_get_group",
		"Fetch a LinkedIn group's metadata by ID",
		"GetGroup",
		getGroup,
	),
	mcptool.Define[*linkedin.Client, GetGroupPostsInput](
		"linkedin_get_group_posts",
		"Fetch posts from a LinkedIn group's feed",
		"GetGroupPosts",
		getGroupPosts,
	),
	mcptool.Define[*linkedin.Client, GetGroupMembersInput](
		"linkedin_get_group_members",
		"Fetch members of a LinkedIn group, optionally filtered by role",
		"GetGroupMembers",
		getGroupMembers,
	),
	mcptool.Define[*linkedin.Client, GetMyGroupsInput](
		"linkedin_get_my_groups",
		"List the groups the authenticated user belongs to",
		"GetMyGroups",
		getMyGroups,
	),
	mcptool.Define[*linkedin.Client, JoinGroupInput](
		"linkedin_join_group",
		"Send a membership request to join a LinkedIn group",
		"JoinGroup",
		joinGroup,
	),
	mcptool.Define[*linkedin.Client, LeaveGroupInput](
		"linkedin_leave_group",
		"Leave a LinkedIn group the authenticated user belongs to",
		"LeaveGroup",
		leaveGroup,
	),
	mcptool.Define[*linkedin.Client, CreateGroupPostInput](
		"linkedin_create_group_post",
		"Post a plain-text message to a LinkedIn group (requires membership)",
		"CreateGroupPost",
		createGroupPost,
	),
}
