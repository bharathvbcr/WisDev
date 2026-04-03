package rag

import (
	"fmt"
	"regexp"
	"strings"
)

// Section represents a detected section in a paper.
type Section struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

var sectionHeaders = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^(abstract|summary)[:.]?\s*`),
	regexp.MustCompile(`(?i)^(introduction|background)[:.]?\s*`),
	regexp.MustCompile(`(?i)^(methods?|methodology|materials?\s*(and|&)\s*methods?)[:.]?\s*`),
	regexp.MustCompile(`(?i)^(results?|findings?)[:.]?\s*`),
	regexp.MustCompile(`(?i)^(discussion)[:.]?\s*`),
	regexp.MustCompile(`(?i)^(conclusion|conclusions?|summary)[:.]?\s*`),
	regexp.MustCompile(`(?i)^(references?|bibliography)[:.]?\s*`),
}

func detectSectionType(line string) string {
	if sectionHeaders[0].MatchString(line) { return "abstract" }
	if sectionHeaders[1].MatchString(line) { return "introduction" }
	if sectionHeaders[2].MatchString(line) { return "methods" }
	if sectionHeaders[3].MatchString(line) { return "results" }
	if sectionHeaders[4].MatchString(line) { return "discussion" }
	if sectionHeaders[5].MatchString(line) { return "conclusion" }
	if sectionHeaders[6].MatchString(line) { return "references" }
	return ""
}

// DetectSections splits text into sections based on common academic headers.
func DetectSections(text string) []Section {
	var sections []Section
	lines := strings.Split(text, "\n")
	
	currentSection := Section{
		Type:    "unknown",
		Content: "",
	}
	
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		secType := detectSectionType(trimmed)
		
		if secType != "" {
			if strings.TrimSpace(currentSection.Content) != "" {
				sections = append(sections, currentSection)
			}
			currentSection = Section{
				Type:    secType,
				Content: line + "\n",
			}
		} else {
			currentSection.Content += line + "\n"
		}
	}
	
	if strings.TrimSpace(currentSection.Content) != "" {
		sections = append(sections, currentSection)
	}
	
	if len(sections) == 0 {
		return []Section{{Type: "unknown", Content: text}}
	}
	return sections
}

// AdaptiveChunking chunks text based on sections and size constraints.
func AdaptiveChunking(text string, paperID string, initialSize int, overlap int) []ChunkDetails {
	sections := DetectSections(text)
	var allChunks []ChunkDetails
	chunkIndex := 0
	
	for _, sec := range sections {
		secChunks := chunkText(sec.Content, paperID, initialSize, overlap, sec.Type, chunkIndex)
		allChunks = append(allChunks, secChunks...)
		chunkIndex += len(secChunks)
	}
	
	return allChunks
}

func chunkText(text string, paperID string, size int, overlap int, sectionType string, startIndex int) []ChunkDetails {
	// Heuristic: 1 token approx 4 characters
	charSize := size * 4
	charOverlap := overlap * 4
	
	if len(text) <= charSize {
		return []ChunkDetails{{
			ID:      fmt.Sprintf("%s_chunk_%d", paperID, startIndex),
			Content: strings.TrimSpace(text),
		}}
	}
	
	var chunks []ChunkDetails
	step := charSize - charOverlap
	if step <= 0 {
		step = 1
	}
	
	pos := 0
	num := 0
	for pos < len(text) {
		end := pos + charSize
		if end > len(text) {
			end = len(text)
		}
		
		content := strings.TrimSpace(text[pos:end])
		if content != "" {
			chunks = append(chunks, ChunkDetails{
				ID:      fmt.Sprintf("%s_chunk_%d", paperID, startIndex+num),
				Content: content,
			})
			num++
		}
		
		pos += step
		if pos >= len(text) {
			break
		}
	}
	
	return chunks
}
