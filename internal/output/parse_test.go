package output

import "testing"

// TestParseResponseTable pins the marker-based response protocol described
// in AGENTS.md: <action>...</action><content>...</content>, or a skip marker.
// Each case names a real-world shape the model is expected to (or must not)
// produce; the parser survives model switches precisely because it tolerates
// preamble and only anchors on these two tags.
func TestParseResponseTable(t *testing.T) {
	cases := []struct {
		name        string
		response    string
		skipMarker  string
		wantAction  string
		wantContent string
		wantOK      bool
	}{
		{
			name:        "happy path",
			response:    "<action>update</action><content>new text</content>",
			wantAction:  "update",
			wantContent: "new text",
			wantOK:      true,
		},
		{
			name:        "preamble before tags is ignored",
			response:    "Sure, here is my answer:\n<action>update</action><content>new text</content>",
			wantAction:  "update",
			wantContent: "new text",
			wantOK:      true,
		},
		{
			name:        "content spans multiple lines and trims whitespace",
			response:    "<action>update</action><content>\n  line one\n  line two\n</content>",
			wantAction:  "update",
			wantContent: "line one\n  line two",
			wantOK:      true,
		},
		{
			name:        "action value is trimmed",
			response:    "<action> update </action><content>c</content>",
			wantAction:  "update",
			wantContent: "c",
			wantOK:      true,
		},
		{
			name:       "skip marker present short-circuits before parsing tags",
			response:   "NO_CHANGES_NEEDED\n<action>update</action><content>c</content>",
			skipMarker: "NO_CHANGES_NEEDED",
			wantOK:     false,
		},
		{
			name:        "skip marker absent falls through to tag parsing",
			response:    "<action>update</action><content>c</content>",
			skipMarker:  "NO_CHANGES_NEEDED",
			wantAction:  "update",
			wantContent: "c",
			wantOK:      true,
		},
		{
			name:       "empty skip marker never matches",
			response:   "",
			skipMarker: "",
			wantOK:     false,
		},
		{
			name:     "missing action tag",
			response: "<content>c</content>",
			wantOK:   false,
		},
		{
			name:     "missing content tag",
			response: "<action>update</action>",
			wantOK:   false,
		},
		{
			name:     "no tags at all",
			response: "NO_CHANGES_NEEDED",
			wantOK:   false,
		},
		{
			name:     "empty response",
			response: "",
			wantOK:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotAction, gotContent, gotOK := ParseResponse(tc.response, tc.skipMarker)
			if gotOK != tc.wantOK {
				t.Fatalf("ParseResponse(%q, %q): ok = %v, want %v", tc.response, tc.skipMarker, gotOK, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if gotAction != tc.wantAction {
				t.Errorf("action = %q, want %q", gotAction, tc.wantAction)
			}
			if gotContent != tc.wantContent {
				t.Errorf("content = %q, want %q", gotContent, tc.wantContent)
			}
		})
	}
}
