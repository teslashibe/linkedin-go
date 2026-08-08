package linkedin

import "testing"

func TestLooksLikePostRejectsSideEntities(t *testing.T) {
	cases := []struct {
		urn  string
		typ  string
		want bool
	}{
		{"urn:li:fs_updateV2:(urn:li:activity:1,MEMBER_SHARES,DEFAULT,DEFAULT,false)", "com.linkedin.voyager.feed.render.UpdateV2", true},
		{"urn:li:activity:1", "com.linkedin.voyager.feed.render.UpdateV2", true},
		{"urn:li:fs_miniCompany:1", "com.linkedin.voyager.entities.shared.MiniCompany", false},
		{"urn:li:fs_socialDetail:urn:li:activity:1", "com.linkedin.voyager.feed.SocialDetail", false},
		{"urn:li:activity:1", "", false},
	}
	for _, tc := range cases {
		got := looksLikePost(map[string]any{"$type": tc.typ, "entityUrn": tc.urn}, tc.urn)
		if got != tc.want {
			t.Fatalf("urn=%s typ=%s got=%v want=%v", tc.urn, tc.typ, got, tc.want)
		}
	}
}

func TestParsePostsSkipsIncludedNoise(t *testing.T) {
	body := []byte(`{"included":[
		{"entityUrn":"urn:li:fs_miniCompany:1","$type":"com.linkedin.voyager.entities.shared.MiniCompany","name":"Acme"},
		{"entityUrn":"urn:li:fs_updateV2:(urn:li:activity:9,MEMBER_SHARES,DEFAULT,DEFAULT,false)","$type":"com.linkedin.voyager.feed.render.UpdateV2","commentary":{"text":{"text":"hello world"}},"updateMetadata":{"urn":"urn:li:activity:9"}},
		{"entityUrn":"urn:li:activity:9","$type":"com.linkedin.voyager.feed.SocialActivityCounts"}
	]}`)
	posts, err := parsePosts(body, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 || posts[0].Text != "hello world" {
		t.Fatalf("posts=%#v", posts)
	}
}

func TestParseMemberComments(t *testing.T) {
	body := []byte(`{"included":[{
		"$type":"com.linkedin.voyager.feed.Comment",
		"entityUrn":"urn:li:comment:(ugcPost:1,2)",
		"*socialDetail":"urn:li:fs_socialDetail:urn:li:comment:(ugcPost:123,456)",
		"commentV2":{"text":"Congrats!"},
		"commenter":{"*miniProfile":"urn:li:fs_miniProfile:X","name":"Ada"}
	}]}`)
	comments, err := parseMemberComments(body, "urn:li:fsd_profile:X")
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].Text != "Congrats!" || comments[0].PostURN != "urn:li:ugcPost:123" {
		t.Fatalf("comments=%#v", comments)
	}
}
