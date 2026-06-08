package rag

import (
	"fmt"
	"regexp"
	"strings"
)

// Section represents a detected section in a paper.
type Section struct {
	Type    string `json:"type"` // abstract, introduction, methods, results, discussion, conclusion, references, table, figure, code
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

var structuredMarkers = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^(table|tab\.)\s+\d+[:.]?\s*`),
	regexp.MustCompile(`(?i)^(figure|fig\.)\s+\d+[:.]?\s*`),
}

func detectSectionType(line string) string {
	for i, re := range sectionHeaders {
		if re.MatchString(line) {
			types := []string{"abstract", "introduction", "methods", "results", "discussion", "conclusion", "references"}
			return types[i]
		}
	}
	
	for i, re := range structuredMarkers {
		if re.MatchString(line) {
			types := []string{"table", "figure"}
			return types[i]
		}
	}

	return ""
}

// DetectSections splits text into sections based on common academic headers and structured elements.
func DetectSections(text string) []Section {
	var sections []Section
	lines := strings.Split(text, "\n")
	
	currentSection := Section{
		Type:    "unknown",
		Content: "",
	}
	
	inCodeBlock := false
	
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		
		// Check for code block boundaries
		if strings.HasPrefix(trimmed, "```") {
			if !inCodeBlock {
				if strings.TrimSpace(currentSection.Content) != "" {
					sections = append(sections, currentSection)
				}
				currentSection = Section{
					Type:    "code",
					Content: line + "\n",
				}
				inCodeBlock = true
			} else {
				currentSection.Content += line + "\n"
				sections = append(sections, currentSection)
				currentSection = Section{
					Type:    "unknown",
					Content: "",
				}
				inCodeBlock = false
			}
			continue
		}
		
		if inCodeBlock {
			currentSection.Content += line + "\n"
			continue
		}

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
