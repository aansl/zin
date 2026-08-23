package config

import "regexp"

var pathParamRE = regexp.MustCompile(`\{([a-zA-Z0-9_]+)(?:\.\.\.)?\}`)

// ExtractParams returns the path parameter names of a ServeMux pattern,
// e.g. "/user/{id}" -> ["id"], "/files/{path...}" -> ["path"].
func ExtractParams(pattern string) []string {
	matches := pathParamRE.FindAllStringSubmatch(pattern, -1)
	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = m[1]
	}
	return out
}
