package mcp

import (
	"context"
	"strconv"

	linkedin "github.com/teslashibe/linkedin-go"
	"github.com/teslashibe/mcptool"
)

type GetPostInput struct {
	PostURN string `json:"post_urn" jsonschema:"description=LinkedIn post URN activity URN or post URL,required"`
}

type GetPostCommentsInput struct {
	PostURN string `json:"post_urn" jsonschema:"description=LinkedIn post or activity URN,required"`
	Start   int    `json:"start,omitempty" jsonschema:"description=pagination offset,minimum=0,default=0"`
	Count   int    `json:"count,omitempty" jsonschema:"description=comments per page,minimum=1,maximum=50,default=20"`
}

type GetUserPostsInput struct {
	Member string `json:"member" jsonschema:"description=LinkedIn member/profile URN or vanity name,required"`
	Start  int    `json:"start,omitempty" jsonschema:"description=pagination offset,minimum=0,default=0"`
	Count  int    `json:"count,omitempty" jsonschema:"description=posts per page,minimum=1,maximum=20,default=5"`
}

type GetUserCommentsInput struct {
	Member string `json:"member" jsonschema:"description=LinkedIn member/profile URN or vanity name,required"`
	Start  int    `json:"start,omitempty" jsonschema:"description=pagination offset,minimum=0,default=0"`
	Count  int    `json:"count,omitempty" jsonschema:"description=comments per page,minimum=1,maximum=20,default=10"`
}

func getPost(ctx context.Context, c *linkedin.Client, in GetPostInput) (any, error) {
	return c.GetPost(ctx, in.PostURN)
}

func getPostComments(ctx context.Context, c *linkedin.Client, in GetPostCommentsInput) (any, error) {
	items, err := c.GetPostComments(ctx, linkedin.PostCommentParams{PostURN: in.PostURN, Start: in.Start, Count: in.Count})
	if err != nil {
		return nil, err
	}
	limit := in.Count
	if limit <= 0 {
		limit = 20
	}
	next := ""
	if len(items) == limit {
		next = strconv.Itoa(in.Start + len(items))
	}
	return mcptool.PageOf(items, next, limit), nil
}

func getUserPosts(ctx context.Context, c *linkedin.Client, in GetUserPostsInput) (any, error) {
	items, err := c.GetUserPosts(ctx, linkedin.UserPostParams{Member: in.Member, Start: in.Start, Count: in.Count})
	if err != nil {
		return nil, err
	}
	limit := in.Count
	if limit <= 0 {
		limit = 5
	}
	next := ""
	if len(items) == limit {
		next = strconv.Itoa(in.Start + len(items))
	}
	return mcptool.PageOf(items, next, limit), nil
}

func getUserComments(ctx context.Context, c *linkedin.Client, in GetUserCommentsInput) (any, error) {
	items, err := c.GetUserComments(ctx, linkedin.UserCommentParams{Member: in.Member, Start: in.Start, Count: in.Count})
	if err != nil {
		return nil, err
	}
	limit := in.Count
	if limit <= 0 {
		limit = 10
	}
	next := ""
	if len(items) == limit {
		next = strconv.Itoa(in.Start + len(items))
	}
	return mcptool.PageOf(items, next, limit), nil
}

var postTools = []mcptool.Tool{
	mcptool.Define[*linkedin.Client, GetPostInput](
		"linkedin_get_post",
		"Fetch a LinkedIn post by post URN, activity URN, or URL.",
		"GetPost",
		getPost,
	),
	mcptool.Define[*linkedin.Client, GetPostCommentsInput](
		"linkedin_get_post_comments",
		"Fetch an offset-paginated page of comments on a LinkedIn post.",
		"GetPostComments",
		getPostComments,
	),
	mcptool.Define[*linkedin.Client, GetUserPostsInput](
		"linkedin_get_user_posts",
		"Fetch recent public posts by LinkedIn member URN or vanity name.",
		"GetUserPosts",
		getUserPosts,
	),
	mcptool.Define[*linkedin.Client, GetUserCommentsInput](
		"linkedin_get_user_comments",
		"Fetch recent comments authored by a LinkedIn member across LinkedIn.",
		"GetUserComments",
		getUserComments,
	),
}
