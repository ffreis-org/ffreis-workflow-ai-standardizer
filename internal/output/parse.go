package output

import (
	"regexp"
	"strings"
)

var (
	actionRe  = regexp.MustCompile(`(?s)<action>(.*?)</action>`)
	contentRe = regexp.MustCompile(`(?s)<content>\s*(.*?)\s*</content>`)
)

// ParseResponse extracts the action and content from the model response.
// Returns ok=false if the skip marker is present or no action block found.
func ParseResponse(response, skipMarker string) (action, content string, ok bool) {
	if skipMarker != "" && strings.Contains(response, skipMarker) {
		return "", "", false
	}
	am := actionRe.FindStringSubmatch(response)
	if am == nil {
		return "", "", false
	}
	cm := contentRe.FindStringSubmatch(response)
	if cm == nil {
		return "", "", false
	}
	return strings.TrimSpace(am[1]), strings.TrimSpace(cm[1]), true
}
